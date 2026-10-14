package query

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
)

// Explain makes every fakeSession a driver.Explainer. It records
// "EXPLAIN [ANALYZE] text" with the executed statements and behaves as the
// statement's fakeStmt says.
func (s *fakeSession) Explain(ctx context.Context, st driver.Statement, analyze bool) (*driver.PlanNode, error) {
	cmd := "EXPLAIN "
	if analyze {
		cmd += "ANALYZE "
	}
	s.mu.Lock()
	s.executed = append(s.executed, cmd+st.Text)
	s.mu.Unlock()
	fs := s.conn.stmts[st.Text]
	s.mu.Lock()
	s.msgs = append(s.msgs, fs.msgs...)
	s.mu.Unlock()
	if fs.blockRun {
		<-ctx.Done()
		return nil, &driver.QueryError{Msg: "canceling statement due to user request", Code: "57014", Position: -1}
	}
	if fs.err != nil {
		return nil, fs.err
	}
	n := &driver.PlanNode{Op: "Seq Scan", Target: st.Text, Cost: 10, Rows: 5, Actual: -1, ActRows: -1, Props: map[string]string{"Filter": "(x > 1)"}}
	if analyze {
		n.Actual, n.ActRows = 1.5, 4
	}
	return &driver.PlanNode{Op: "Limit", Cost: 11, Rows: 5, Actual: n.Actual, ActRows: n.ActRows, Children: []*driver.PlanNode{n}}, nil
}

func (e *env) explain(ctx context.Context, req *quarryv1.ExplainRequest) (*quarryv1.ExplainResponse, error) {
	req.TabId = e.tab
	res, err := e.s.Explain(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func explainAt(script string, i int32) *quarryv1.ExplainRequest {
	return &quarryv1.ExplainRequest{Script: script, Mode: quarryv1.RunMode_RUN_MODE_STATEMENT, Start: i}
}

func TestExplain(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{"select 2": {msgs: []driver.Message{{Severity: "NOTICE", Text: "hi"}}}})
	script := "select 1;\nselect 2"
	res, err := e.explain(context.Background(), explainAt(script, 12))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Plan
	if p.Op != "Limit" || p.Actual != -1 || len(p.Children) != 1 || p.Children[0].Target != "select 2" || p.Children[0].Props["Filter"] != "(x > 1)" {
		t.Fatalf("plan = %v", p)
	}
	if res.Range.Start != 10 || res.Range.End != 18 || res.Error != nil || !res.Tx.Supported {
		t.Fatalf("response = %v", res)
	}
	if len(res.Messages) != 1 || res.Messages[0].Text != "hi" {
		t.Fatalf("messages = %v", res.Messages)
	}

	req := explainAt(script, 0)
	req.Analyze = true
	res, err = e.explain(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Actual != 1.5 || res.Plan.Children[0].ActRows != 4 {
		t.Fatalf("analyze plan = %v", res.Plan)
	}
	if got := e.conn.sessionsRan(); !slices.Equal(got, []string{"EXPLAIN select 2", "EXPLAIN ANALYZE select 1"}) {
		t.Fatalf("ran = %q", got)
	}
	if len(e.hist.all()) != 0 {
		t.Fatal("explain recorded history")
	}
}

func TestExplainSelection(t *testing.T) {
	e := newEnv(t, nil)
	script := "select 1;\nselect 2"
	for _, req := range []*quarryv1.ExplainRequest{
		{Script: script, Mode: quarryv1.RunMode_RUN_MODE_RANGE, Start: 0, End: int32(len(script))},
		{Script: "  ", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT},
		{Script: "select :id", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT},
	} {
		if _, err := e.explain(context.Background(), req); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%q: %v", req.Script, err)
		}
	}
	res, err := e.explain(context.Background(), &quarryv1.ExplainRequest{
		Script: script, Mode: quarryv1.RunMode_RUN_MODE_RANGE, Start: 12, End: 14,
	})
	if err != nil || res.Range.Start != 10 {
		t.Fatalf("range inside one statement: %v %v", res, err)
	}
	req := explainAt("select :id", 0)
	req.Params = map[string]string{"id": "7"}
	if _, err := e.explain(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := e.conn.sessionsRan(); len(got) != 2 || got[1] == "EXPLAIN select :id" {
		t.Fatalf("ran = %q", got)
	}
}

func TestExplainGuards(t *testing.T) {
	e := newEnv(t, nil)
	e.live.configure("pg", func(c *driver.Config) { c.Env = "prod" })
	write := "update t set x = 1 where id = 2"
	if _, err := e.explain(context.Background(), explainAt(write, 0)); err != nil {
		t.Fatalf("plain explain of a write on prod: %v", err)
	}
	req := explainAt(write, 0)
	req.Analyze = true
	if _, err := e.explain(context.Background(), req); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("analyze of a write on prod without confirmation: %v", err)
	}
	req.Confirmed = true
	if _, err := e.explain(context.Background(), req); err != nil {
		t.Fatalf("confirmed: %v", err)
	}
	read := explainAt("select 1", 0)
	read.Analyze = true
	if _, err := e.explain(context.Background(), read); err != nil {
		t.Fatalf("analyze of a read on prod: %v", err)
	}

	e.live.configure("pg", func(c *driver.Config) { c.Env = "dev" })
	unbounded := explainAt("delete from t", 0)
	unbounded.Analyze = true
	if _, err := e.explain(context.Background(), unbounded); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("analyze of an unbounded delete: %v", err)
	}

	e.live.configure("pg", func(c *driver.Config) { c.ReadOnly = true })
	req.Confirmed = true
	if _, err := e.explain(context.Background(), req); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("analyze of a write on a read-only connection: %v", err)
	}
	if _, err := e.explain(context.Background(), explainAt(write, 0)); err != nil {
		t.Fatalf("plain explain on a read-only connection: %v", err)
	}
	want := []string{"EXPLAIN " + write, "EXPLAIN ANALYZE " + write, "EXPLAIN ANALYZE select 1", "EXPLAIN " + write}
	if got := e.conn.sessionsRan(); !slices.Equal(got, want) {
		t.Fatalf("ran = %q", got)
	}
}

func TestExplainServerError(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{
		"select nope": {err: &driver.QueryError{Msg: `column "nope" does not exist`, Code: "42703", Position: 7}},
	})
	res, err := e.explain(context.Background(), explainAt("select 1;\nselect nope", 12))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil || res.Error == nil || res.Error.Code != "42703" || res.Error.Position != 17 {
		t.Fatalf("response = %v", res)
	}
}

func TestExplainUnimplemented(t *testing.T) {
	e := newEnv(t, nil)
	plain := newFakeConn(map[string]fakeStmt{"select big": {cols: 1, rows: 50}})
	plain.noTx = true
	e.live.add("pg", driver.Postgres, plain)
	e.runAll(t, "select big", 10)
	if _, err := e.explain(context.Background(), explainAt("select 1", 0)); code(err) != connect.CodeUnimplemented {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.fetch(t, 0); err != nil {
		t.Fatalf("unimplemented explain discarded the open result: %v", err)
	}
}

func TestExplainDiscardsOpenResult(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{"select big": {cols: 1, rows: 50}})
	e.runAll(t, "select big", 10)
	if _, err := e.explain(context.Background(), explainAt("select 1", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fetch(t, 0); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("explain should discard the open result: %v", err)
	}
}

func TestExplainBusyAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select pg_sleep(30)": {blockRun: true}})
		done := make(chan error)
		go func() {
			_, err := e.explain(context.Background(), &quarryv1.ExplainRequest{Script: "select pg_sleep(30)", Analyze: true})
			done <- err
		}()
		synctest.Wait()
		if _, err := e.run(context.Background(), "select 1", quarryv1.RunMode_RUN_MODE_STATEMENT, 0, 0, 0); code(err) != connect.CodeFailedPrecondition {
			t.Fatalf("run while explaining: %v", err)
		}
		if _, err := e.explain(context.Background(), explainAt("select 1", 0)); code(err) != connect.CodeFailedPrecondition {
			t.Fatalf("explain while explaining: %v", err)
		}
		e.cancel(t)
		if err := <-done; code(err) != connect.CodeCanceled {
			t.Fatalf("canceled explain: %v", err)
		}
		if _, err := e.explain(context.Background(), explainAt("select 1", 0)); err != nil {
			t.Fatalf("explain after cancel: %v", err)
		}
	})
}

func TestITExplain(t *testing.T) {
	e := newPGEnv(t)
	e.runAll(t, "create temp table explain_it (id int primary key, v text); insert into explain_it values (1, 'a')", 0)
	req := explainAt("update explain_it set v = 'b' where id = 1", 0)
	req.Analyze = true
	res, err := e.explain(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p := res.Plan; p == nil || p.Op != "Update" || p.Actual < 0 || p.Props["Execution Time"] == "" || res.Tx.InTx {
		t.Fatalf("response = %v", res)
	}
	evs := e.runAll(t, "select v from explain_it", 0)
	if v := evs[3].GetRows().Rows[0].Values[0].GetText(); v != "a" {
		t.Fatalf("analyze was not rolled back: %q", v)
	}
	res, err = e.explain(context.Background(), explainAt("select 1;\nselect nope from explain_it", 12))
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == nil || res.Error.Position != 17 {
		t.Fatalf("error = %v", res.Error)
	}
}
