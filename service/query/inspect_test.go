package query

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
)

// runReq runs req on the env's tab unless it names another.
func (e *env) runReq(req *quarryv1.RunRequest) ([]*quarryv1.RunEvent, error) {
	if req.TabId == "" {
		req.TabId = e.tab
	}
	if req.Mode == quarryv1.RunMode_RUN_MODE_UNSPECIFIED {
		req.Mode, req.End = quarryv1.RunMode_RUN_MODE_RANGE, int32(len(req.Script))
	}
	var evs []*quarryv1.RunEvent
	err := e.s.run(context.Background(), req, func(ev *quarryv1.RunEvent) error { evs = append(evs, ev); return nil })
	return evs, err
}

func (e *env) inspect(t *testing.T, tab, script string) *quarryv1.InspectResponse {
	t.Helper()
	res, err := e.s.Inspect(context.Background(), connect.NewRequest(&quarryv1.InspectRequest{
		TabId: tab, Script: script, Mode: quarryv1.RunMode_RUN_MODE_RANGE, End: int32(len(script)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg
}

func message(err error) string {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Message()
	}
	return ""
}

func TestInspect(t *testing.T) {
	e := newEnv(t, nil)
	script := "select 1;\ninsert into t values (:a);\ndelete from t;\ndrop table t;\ncreate table u (x int);\nupdate t set x = $1 where id = :a"
	res := e.inspect(t, e.tab, script)
	type check struct{ risk, verb string }
	var got []check
	for _, c := range res.Statements {
		got = append(got, check{c.Risk, c.Verb})
		if (c.Risk == "none") != (c.Reason == "") {
			t.Errorf("%s: reason %q", c.Verb, c.Reason)
		}
	}
	want := []check{{"none", "SELECT"}, {"write", "INSERT"}, {"unbounded_write", "DELETE"}, {"destructive", "DROP TABLE"}, {"ddl", "CREATE TABLE"}, {"write", "UPDATE"}}
	if !slices.Equal(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	if r := res.Statements[2].Range; r.Start != 37 || r.End != 50 {
		t.Fatalf("range = %v", r)
	}
	if !slices.Equal(res.Params, []string{"a", "1"}) || !res.NeedsConfirmation || res.Blocked || res.Env != "" {
		t.Fatalf("inspect = %v", res)
	}

	one, err := e.s.Inspect(context.Background(), connect.NewRequest(&quarryv1.InspectRequest{
		TabId: e.tab, Script: script, Mode: quarryv1.RunMode_RUN_MODE_STATEMENT, Start: 12,
	}))
	if err != nil || len(one.Msg.Statements) != 1 || one.Msg.Statements[0].Verb != "INSERT" || one.Msg.NeedsConfirmation {
		t.Fatalf("inspect statement = %v, %v", one, err)
	}

	// No open connection or session is needed.
	e.live.add("off", driver.Postgres, nil)
	e.live.configure("off", func(c *driver.Config) { c.Env = "qa" })
	if res := e.inspect(t, e.open(t, "off"), "select 1"); res.Env != "qa" || len(res.Statements) != 1 {
		t.Fatalf("inspect offline = %v", res)
	}
	if e.conn.count() != 0 {
		t.Fatal("inspect opened a session")
	}
	if _, err := e.s.Inspect(context.Background(), connect.NewRequest(&quarryv1.InspectRequest{TabId: "nope"})); code(err) != connect.CodeNotFound {
		t.Fatalf("inspect unknown tab: %v", err)
	}
}

func TestConfirmation(t *testing.T) {
	stmts := []struct {
		text      string
		prod, dev bool
	}{
		{"select 1", false, false},
		{"insert into t values (1)", true, false},
		{"update t set x = 1 where id = 2", true, false},
		{"create table u (x int)", true, false},
		{"delete from t", true, true},
		{"update t set x = 1", true, true},
		{"drop table t", true, true},
		{"truncate t", true, true},
	}
	for _, env := range []string{"prod", "dev"} {
		e := newEnv(t, nil)
		e.live.configure("pg", func(c *driver.Config) { c.Env = env })
		for _, st := range stmts {
			want := st.dev
			if env == "prod" {
				want = st.prod
			}
			res := e.inspect(t, e.tab, st.text)
			if res.NeedsConfirmation != want || res.Env != env {
				t.Errorf("%s %q: needs_confirmation = %v", env, st.text, res.NeedsConfirmation)
			}
			ran := len(e.conn.sessionsRan())
			evs, err := e.runReq(&quarryv1.RunRequest{Script: st.text})
			if want {
				if code(err) != connect.CodeFailedPrecondition || !strings.HasPrefix(message(err), "confirmation required: ") || len(evs) != 0 {
					t.Errorf("%s %q unconfirmed: %v, %v", env, st.text, kinds(evs), err)
				}
				if len(e.conn.sessionsRan()) != ran {
					t.Errorf("%s %q executed without confirmation", env, st.text)
				}
			} else if err != nil {
				t.Errorf("%s %q: %v", env, st.text, err)
			}
			evs, err = e.runReq(&quarryv1.RunRequest{Script: st.text, Confirmed: true})
			if err != nil || errorEvent(evs) != nil || e.conn.sessionsRan()[len(e.conn.sessionsRan())-1] != st.text {
				t.Errorf("%s %q confirmed: %v, %v", env, st.text, kinds(evs), err)
			}
		}
	}

	e := newEnv(t, nil)
	_, err := e.runReq(&quarryv1.RunRequest{Script: "select 1;\ndrop table t;\ndelete from u"})
	if msg := message(err); msg != "confirmation required: DROP TABLE removes t permanently" {
		t.Fatalf("message = %q", msg)
	}
}

func TestReadOnlyBlocks(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{"select 1": {cols: 1, rows: 1}})
	e.live.configure("pg", func(c *driver.Config) { c.ReadOnly = true })
	for script, blocked := range map[string]bool{
		"select 1":                           false,
		"select 1; insert into t values (1)": true,
		"call refresh()":                     true,
		"create table u (x int)":             true,
	} {
		if res := e.inspect(t, e.tab, script); res.Blocked != blocked {
			t.Errorf("%q: blocked = %v", script, res.Blocked)
		}
	}
	evs, err := e.runReq(&quarryv1.RunRequest{Script: "select 1; insert into t values (1)", Confirmed: true})
	if code(err) != connect.CodeFailedPrecondition || message(err) != "connection is read-only" || len(evs) != 0 {
		t.Fatalf("run on read-only = %v, %v", kinds(evs), err)
	}
	if e.conn.count() != 0 {
		t.Fatal("blocked run opened a session")
	}
	if evs, err := e.runReq(&quarryv1.RunRequest{Script: "select 1"}); err != nil || errorEvent(evs) != nil {
		t.Fatalf("read on read-only = %v, %v", kinds(evs), err)
	}
}

func TestParams(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{
		"select 1 + 'a''b', ':y'": {cols: 1, rows: 1},
		"select nosuch, 7":        {err: &driver.QueryError{Msg: "column does not exist", Position: 7}},
	})
	script := "select :x + $1, ':y'"
	evs, err := e.runReq(&quarryv1.RunRequest{Script: script, Params: map[string]string{"x": "1", "1": "a'b"}})
	if err != nil || errorEvent(evs) != nil {
		t.Fatalf("run = %v, %v", kinds(evs), err)
	}
	if ran := e.conn.session(0).ran(); !slices.Equal(ran, []string{"select 1 + 'a''b', ':y'"}) {
		t.Fatalf("ran %q", ran)
	}
	if r := evs[1].GetStatement().Range; r.Start != 0 || r.End != int32(len(script)) {
		t.Fatalf("range = %v", r)
	}
	if h := e.hist.all(); h[0].Text != "select 1 + 'a''b', ':y'" {
		t.Fatalf("history = %q", h[0].Text)
	}

	evs, err = e.runReq(&quarryv1.RunRequest{Script: "select :x;\nselect :y, $2, :x, :y", Params: map[string]string{"x": "1"}})
	if code(err) != connect.CodeInvalidArgument || message(err) != "no value for parameter :y, $2" || len(evs) != 0 {
		t.Fatalf("missing params = %v, %v", kinds(evs), err)
	}
	if len(e.conn.session(0).ran()) != 1 {
		t.Fatal("ran with missing params")
	}

	// Positions in the substituted text do not map to the script.
	evs, err = e.runReq(&quarryv1.RunRequest{Script: "select nosuch, :n", Params: map[string]string{"n": "7"}})
	if qe := errorEvent(evs); err != nil || qe == nil || qe.Position != -1 || qe.Message != "column does not exist" {
		t.Fatalf("error = %v, %v", qe, err)
	}
	// Statements without parameters keep their positions.
	e.conn.stmts["select nosuch"] = fakeStmt{err: &driver.QueryError{Msg: "column does not exist", Position: 7}}
	evs, _ = e.runReq(&quarryv1.RunRequest{Script: "select nosuch", Params: map[string]string{"n": "7"}})
	if qe := errorEvent(evs); qe == nil || qe.Position != 7 {
		t.Fatalf("error = %v", qe)
	}
}
