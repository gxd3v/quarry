package query

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

func sqlLang(driver.Kind) (driver.Language, bool) { return driver.LangSQL, true }

type env struct {
	s    *Service
	live *fakeLive
	conn *fakeConn
	hist *memHistory
	tab  string
}

func newEnv(t *testing.T, stmts map[string]fakeStmt, opts ...Option) *env {
	t.Helper()
	e := &env{live: newFakeLive(), conn: newFakeConn(stmts), hist: &memHistory{}}
	e.live.add("pg", driver.Postgres, e.conn)
	e.s = New(e.live, append([]Option{WithLanguage(sqlLang), WithHistory(e.hist)}, opts...)...)
	t.Cleanup(e.s.Close)
	e.tab = e.open(t, "pg")
	return e
}

func (e *env) open(t *testing.T, conn string) string {
	t.Helper()
	res, err := e.s.OpenTab(context.Background(), connect.NewRequest(&quarryv1.OpenTabRequest{ConnId: conn}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.TabId
}

func (e *env) run(ctx context.Context, script string, mode quarryv1.RunMode, start, end, max int32) ([]*quarryv1.RunEvent, error) {
	var evs []*quarryv1.RunEvent
	err := e.s.run(ctx, &quarryv1.RunRequest{TabId: e.tab, Script: script, Mode: mode, Start: start, End: end, MaxRows: max},
		func(ev *quarryv1.RunEvent) error { evs = append(evs, ev); return nil })
	return evs, err
}

// runAll runs every statement of script.
func (e *env) runAll(t *testing.T, script string, max int32) []*quarryv1.RunEvent {
	t.Helper()
	evs, err := e.run(context.Background(), script, quarryv1.RunMode_RUN_MODE_RANGE, 0, int32(len(script)), max)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func (e *env) fetch(t *testing.T, max int32) ([]*quarryv1.RunEvent, error) {
	t.Helper()
	var evs []*quarryv1.RunEvent
	err := e.s.fetch(context.Background(), &quarryv1.FetchRequest{TabId: e.tab, MaxRows: max},
		func(ev *quarryv1.RunEvent) error { evs = append(evs, ev); return nil })
	return evs, err
}

func kinds(evs []*quarryv1.RunEvent) []string {
	var out []string
	for _, ev := range evs {
		var k string
		switch ev.Event.(type) {
		case *quarryv1.RunEvent_Started:
			k = "started"
		case *quarryv1.RunEvent_Statement:
			k = "statement"
		case *quarryv1.RunEvent_Columns:
			k = "columns"
		case *quarryv1.RunEvent_Rows:
			k = "rows"
		case *quarryv1.RunEvent_Done:
			k = "done"
		case *quarryv1.RunEvent_Error:
			k = "error"
		case *quarryv1.RunEvent_Message:
			k = "message"
		case *quarryv1.RunEvent_Finished:
			k = "finished"
		}
		out = append(out, k)
	}
	return out
}

func batches(evs []*quarryv1.RunEvent) []int {
	var out []int
	for _, ev := range evs {
		if r := ev.GetRows(); r != nil {
			out = append(out, len(r.Rows))
		}
	}
	return out
}

func dones(evs []*quarryv1.RunEvent) []*quarryv1.StatementDone {
	var out []*quarryv1.StatementDone
	for _, ev := range evs {
		if d := ev.GetDone(); d != nil {
			out = append(out, d)
		}
	}
	return out
}

func finished(t *testing.T, evs []*quarryv1.RunEvent) *quarryv1.RunFinished {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].GetFinished() == nil {
		t.Fatalf("run did not finish: %v", kinds(evs))
	}
	return evs[len(evs)-1].GetFinished()
}

func errorEvent(evs []*quarryv1.RunEvent) *quarryv1.QueryError {
	for _, ev := range evs {
		if q := ev.GetError(); q != nil {
			return q
		}
	}
	return nil
}

func code(err error) connect.Code { return connect.CodeOf(err) }

func TestRunRangeEventOrder(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{
		"select a": {cols: 2, rows: 3},
		"insert b": {affected: 2, msgs: []driver.Message{{Severity: "NOTICE", Text: "hi"}}},
		"select c": {cols: 1, rows: 1},
	})
	script := "select a;\ninsert b;\nselect c;"
	evs := e.runAll(t, script, 0)
	want := []string{"started", "statement", "columns", "rows", "done", "statement", "done", "message", "statement", "columns", "rows", "done", "finished"}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if st := evs[0].GetStarted(); st.Statements != 3 || st.RunId == "" {
		t.Fatalf("started = %v", st)
	}
	if r := evs[5].GetStatement(); r.Index != 1 || r.Range.Start != 10 || r.Range.End != 18 {
		t.Fatalf("statement 1 = %v", r)
	}
	if m := evs[7].GetMessage(); m.Severity != "NOTICE" || m.Text != "hi" {
		t.Fatalf("message = %v", m)
	}
	d := dones(evs)
	if d[0].Rows != 3 || d[0].HasMore || d[0].Tag != "SELECT 3" || d[1].Affected != 2 || d[2].Rows != 1 {
		t.Fatalf("dones = %v", d)
	}
	if f := finished(t, evs); f.Canceled || !f.Tx.Supported || !f.Tx.AutoCommit {
		t.Fatalf("finished = %v", f)
	}
	h := e.hist.all()
	if len(h) != 3 || h[0].Rows != 3 || h[1].Rows != 2 || h[2].Text != "select c" || h[0].Err != "" {
		t.Fatalf("history = %+v", h)
	}
	if want, _ := e.live.Config("pg"); h[0].Conn != want.ID {
		t.Fatalf("history conn = %v", h[0].Conn)
	}

	// A range touching the second and third statements runs only those, with
	// ranges relative to the whole script.
	evs, err := e.run(context.Background(), script, quarryv1.RunMode_RUN_MODE_RANGE, 14, 22, 0)
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].GetStarted().Statements != 2 || evs[1].GetStatement().Range.Start != 10 || evs[1].GetStatement().Index != 0 {
		t.Fatalf("range run = %v", evs[:2])
	}

	// Statement mode runs the statement at the cursor.
	evs, err = e.run(context.Background(), script, quarryv1.RunMode_RUN_MODE_STATEMENT, 21, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].GetStarted().Statements != 1 || evs[1].GetStatement().Range.Start != 20 {
		t.Fatalf("statement run = %v", evs[:2])
	}
	if ran := e.conn.session(0).ran(); ran[len(ran)-1] != "select c" {
		t.Fatalf("ran %v", ran)
	}
}

func TestRunNoStatements(t *testing.T) {
	e := newEnv(t, nil)
	evs := e.runAll(t, "  -- nothing here\n", 0)
	if got := kinds(evs); !slices.Equal(got, []string{"started", "finished"}) || evs[0].GetStarted().Statements != 0 {
		t.Fatalf("events = %v", got)
	}
}

func TestBatchSizes(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{"select big": {cols: 1, rows: 5000}})
	evs := e.runAll(t, "select big", 0)
	if got := batches(evs); !slices.Equal(got, []int{200, 2000, 2000, 800}) {
		t.Fatalf("batches = %v", got)
	}
	if d := dones(evs)[0]; d.Rows != 5000 || d.HasMore {
		t.Fatalf("done = %v", d)
	}
	if _, err := e.fetch(t, 0); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("fetch after exhausted result: %v", err)
	}
}

func TestMaxRowsTruncatesNonLast(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{
		"select big":   {cols: 1, rows: 5000},
		"select small": {cols: 1, rows: 10},
	}, WithMaxRows(500))
	evs := e.runAll(t, "select big; select small", 0)
	d := dones(evs)
	if d[0].Rows != 500 || !d[0].HasMore || d[1].Rows != 10 || d[1].HasMore {
		t.Fatalf("dones = %v", d)
	}
	if got := batches(evs); !slices.Equal(got, []int{200, 300, 10}) {
		t.Fatalf("batches = %v", got)
	}
	if _, err := e.fetch(t, 0); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("fetch from a truncated non-last result: %v", err)
	}
}

func TestFetch(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{"select big": {cols: 1, rows: 5000}})
	evs := e.runAll(t, "select big", 300)
	if got := batches(evs); !slices.Equal(got, []int{200, 100}) {
		t.Fatalf("batches = %v", got)
	}
	if d := dones(evs)[0]; d.Rows != 300 || !d.HasMore {
		t.Fatalf("done = %v", d)
	}

	evs, err := e.fetch(t, 2500)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(evs); !slices.Equal(got, []string{"rows", "rows", "done", "finished"}) {
		t.Fatalf("fetch events = %v", got)
	}
	if got := batches(evs); !slices.Equal(got, []int{2000, 500}) {
		t.Fatalf("fetch batches = %v", got)
	}
	if d := dones(evs)[0]; d.Rows != 2800 || !d.HasMore {
		t.Fatalf("fetch done = %v", d)
	}
	if first := evs[0].GetRows().Rows[0].Values[0].GetInt(); first != 300 {
		t.Fatalf("fetch resumed at row %d", first)
	}

	evs, err = e.fetch(t, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := batches(evs); !slices.Equal(got, []int{2000, 200}) {
		t.Fatalf("fetch all batches = %v", got)
	}
	if d := dones(evs)[0]; d.Rows != 5000 || d.HasMore {
		t.Fatalf("fetch all done = %v", d)
	}
	if _, err := e.fetch(t, 0); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("fetch after the end: %v", err)
	}
}

func TestNewRunDiscardsOpenResult(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{"select big": {cols: 1, rows: 5000}})
	e.runAll(t, "select big", 10)
	res := e.conn.session(0).cur
	e.runAll(t, "select 1", 0)
	if !res.closed.Load() {
		t.Fatal("open result was not closed by the next run")
	}
	if _, err := e.fetch(t, 0); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("fetch after a new run: %v", err)
	}
}

func TestErrorPosition(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{
		"select nosuch": {
			err:  &driver.QueryError{Msg: "column does not exist", Code: "42703", Detail: "d", Hint: "h", Position: 7},
			msgs: []driver.Message{{Severity: "WARNING", Text: "careful"}},
		},
		"select boom": {err: errors.New("connection reset")},
		"select 2":    {},
	})
	// "-- héllo 😀\n" is 12 UTF-16 units, "select 1;\n" 10 more.
	script := "-- héllo 😀\nselect 1;\nselect nosuch;\nselect 2;"
	evs := e.runAll(t, script, 0)
	want := []string{"started", "statement", "done", "statement", "message", "error", "finished"}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if r := evs[3].GetStatement().Range; r.Start != 22 || r.End != 35 {
		t.Fatalf("range = %v", r)
	}
	qe := errorEvent(evs)
	if qe.Position != 29 || qe.Code != "42703" || qe.Message != "column does not exist" || qe.Detail != "d" || qe.Hint != "h" {
		t.Fatalf("error = %v", qe)
	}
	if slices.Contains(e.conn.session(0).ran(), "select 2") {
		t.Fatal("run continued after an error")
	}
	if h := e.hist.all(); len(h) != 2 || h[1].Err != "column does not exist" {
		t.Fatalf("history = %+v", h)
	}

	evs = e.runAll(t, "select boom", 0)
	if qe := errorEvent(evs); qe.Position != -1 || qe.Message != "connection reset" || qe.Code != "" {
		t.Fatalf("plain error = %v", qe)
	}
}

func TestRowError(t *testing.T) {
	e := newEnv(t, map[string]fakeStmt{
		"select bad": {cols: 1, rows: 5, rowErr: &driver.QueryError{Msg: "division by zero", Position: 0}},
	})
	evs := e.runAll(t, "\n\nselect bad", 0)
	if got := kinds(evs); !slices.Equal(got, []string{"started", "statement", "columns", "rows", "error", "finished"}) {
		t.Fatalf("events = %v", got)
	}
	if qe := errorEvent(evs); qe.Position != 2 {
		t.Fatalf("error = %v", qe)
	}
}

func TestHistorySinkErrorIsIgnored(t *testing.T) {
	e := newEnv(t, nil)
	e.hist.err = errors.New("disk full")
	evs := e.runAll(t, "select 1", 0)
	if f := finished(t, evs); f.Canceled || errorEvent(evs) != nil {
		t.Fatalf("events = %v", kinds(evs))
	}
}

func TestElapsed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select slow": {sleep: 1500 * time.Millisecond}})
		evs := e.runAll(t, "select slow", 0)
		if d := dones(evs)[0]; d.ElapsedMs != 1500 {
			t.Fatalf("done elapsed = %d", d.ElapsedMs)
		}
		if f := finished(t, evs); f.ElapsedMs != 1500 {
			t.Fatalf("finished elapsed = %d", f.ElapsedMs)
		}
		if h := e.hist.all(); h[0].Duration != 1500*time.Millisecond {
			t.Fatalf("history duration = %v", h[0].Duration)
		}
	})
}

type bgRun struct {
	evs  []*quarryv1.RunEvent
	err  error
	done chan struct{}
}

func (e *env) runBG(ctx context.Context, script string) *bgRun {
	r := &bgRun{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.evs, r.err = e.run(ctx, script, quarryv1.RunMode_RUN_MODE_STATEMENT, 0, 0, 0)
	}()
	return r
}

func (e *env) cancel(t *testing.T) {
	t.Helper()
	if _, err := e.s.Cancel(context.Background(), connect.NewRequest(&quarryv1.CancelRequest{TabId: e.tab})); err != nil {
		t.Fatal(err)
	}
}

func TestBusyAndCancelMidStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select slow": {cols: 1, rows: 1000, blockAt: 300}})
		r := e.runBG(context.Background(), "select slow")
		synctest.Wait()

		if _, err := e.run(context.Background(), "select 1", quarryv1.RunMode_RUN_MODE_STATEMENT, 0, 0, 0); code(err) != connect.CodeFailedPrecondition {
			t.Fatalf("second run: %v", err)
		}
		if _, err := e.fetch(t, 0); code(err) != connect.CodeFailedPrecondition {
			t.Fatalf("fetch while busy: %v", err)
		}
		if _, err := e.s.Commit(context.Background(), connect.NewRequest(&quarryv1.CommitRequest{TabId: e.tab})); code(err) != connect.CodeFailedPrecondition {
			t.Fatalf("commit while busy: %v", err)
		}
		if _, err := e.s.Rebind(context.Background(), connect.NewRequest(&quarryv1.RebindRequest{TabId: e.tab, ConnId: "pg"})); code(err) != connect.CodeFailedPrecondition {
			t.Fatalf("rebind while busy: %v", err)
		}

		e.cancel(t)
		<-r.done
		if r.err != nil {
			t.Fatal(r.err)
		}
		if got := batches(r.evs); !slices.Equal(got, []int{200, 100}) {
			t.Fatalf("batches = %v", got)
		}
		if f := finished(t, r.evs); !f.Canceled {
			t.Fatalf("finished = %v", f)
		}
		if errorEvent(r.evs) != nil || len(dones(r.evs)) != 0 {
			t.Fatalf("events = %v", kinds(r.evs))
		}
		if h := e.hist.all(); len(h) != 1 || h[0].Err != "canceled" {
			t.Fatalf("history = %+v", h)
		}

		evs := e.runAll(t, "select 1", 0)
		if f := finished(t, evs); f.Canceled || e.conn.count() != 1 {
			t.Fatalf("run after cancel: %v, sessions %d", kinds(evs), e.conn.count())
		}
		e.cancel(t) // idle: no-op
	})
}

func TestCancelDuringExecute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select pg_sleep(30)": {blockRun: true}})
		r := e.runBG(context.Background(), "select pg_sleep(30)")
		synctest.Wait()
		e.cancel(t)
		<-r.done
		if got := kinds(r.evs); !slices.Equal(got, []string{"started", "statement", "finished"}) || !finished(t, r.evs).Canceled {
			t.Fatalf("events = %v", got)
		}
	})
}

func TestClientGoneCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select slow": {cols: 1, rows: 1000, blockAt: 300}})
		ctx, cancel := context.WithCancel(context.Background())
		r := e.runBG(ctx, "select slow")
		synctest.Wait()
		cancel()
		<-r.done
		if r.err != nil || !finished(t, r.evs).Canceled {
			t.Fatalf("err %v, events %v", r.err, kinds(r.evs))
		}

		// A send failure stops the run too.
		var n int
		err := e.s.run(context.Background(), &quarryv1.RunRequest{TabId: e.tab, Script: "select slow", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT},
			func(*quarryv1.RunEvent) error {
				if n++; n > 3 {
					return errors.New("stream closed")
				}
				return nil
			})
		if err != nil || n != 4 {
			t.Fatalf("err %v after %d sends", err, n)
		}
		e.runAll(t, "select 1", 0)
	})
}

func TestCloseTabWhileRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select slow": {cols: 1, rows: 1000, blockAt: 300}})
		r := e.runBG(context.Background(), "select slow")
		synctest.Wait()
		if _, err := e.s.CloseTab(context.Background(), connect.NewRequest(&quarryv1.CloseTabRequest{TabId: e.tab})); err != nil {
			t.Fatal(err)
		}
		<-r.done
		if !finished(t, r.evs).Canceled || !e.conn.session(0).closed.Load() {
			t.Fatal("close did not cancel the run and close the session")
		}
		if _, err := e.s.CloseTab(context.Background(), connect.NewRequest(&quarryv1.CloseTabRequest{TabId: e.tab})); code(err) != connect.CodeNotFound {
			t.Fatalf("second close: %v", err)
		}

		// Service.Close waits for running operations.
		e.tab = e.open(t, "pg")
		r = e.runBG(context.Background(), "select slow")
		synctest.Wait()
		e.s.Close()
		// The session closes when the run ends, so it proves Close waited.
		if !e.conn.session(1).closed.Load() {
			t.Fatal("Close returned before the run ended")
		}
		synctest.Wait() // the client goroutine finishes reading its stream
		select {
		case <-r.done:
		default:
			t.Fatal("the run did not end")
		}
	})
}

func TestAnalyze(t *testing.T) {
	live := newFakeLive()
	live.add("pg", driver.Postgres, nil) // saved, not connected
	live.add("redis", driver.Redis, nil)
	s := New(live, WithLanguage(sqlLang))
	defer s.Close()
	ctx := context.Background()
	analyze := func(conn, script string, cursor int32) *quarryv1.AnalyzeResponse {
		t.Helper()
		tab, err := s.OpenTab(ctx, connect.NewRequest(&quarryv1.OpenTabRequest{ConnId: conn}))
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Analyze(ctx, connect.NewRequest(&quarryv1.AnalyzeRequest{TabId: tab.Msg.TabId, Script: script, Cursor: cursor}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}

	script := "select '😀';\nselect 'é';\n"
	res := analyze("pg", script, 13)
	if len(res.Statements) != 2 || res.Current != 1 {
		t.Fatalf("analyze = %v", res)
	}
	if r := res.Statements[0]; r.Start != 0 || r.End != 11 {
		t.Fatalf("range 0 = %v", r)
	}
	if r := res.Statements[1]; r.Start != 13 || r.End != 23 {
		t.Fatalf("range 1 = %v", r)
	}
	if res := analyze("pg", "  ", 0); len(res.Statements) != 0 || res.Current != -1 {
		t.Fatalf("empty analyze = %v", res)
	}

	res = analyze("redis", "\n  GET k\nSET k v  \n", 0)
	// One command per line.
	if len(res.Statements) != 2 || res.Current != 0 || res.Statements[0].Start != 3 || res.Statements[0].End != 8 ||
		res.Statements[1].Start != 9 || res.Statements[1].End != 16 {
		t.Fatalf("redis analyze = %v", res)
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	e.live.add("off", driver.Postgres, nil)
	broken := newFakeConn(nil)
	broken.openErr = errors.New("too many connections")
	e.live.add("broken", driver.Postgres, broken)

	if _, err := e.s.OpenTab(ctx, connect.NewRequest(&quarryv1.OpenTabRequest{ConnId: "nope"})); code(err) != connect.CodeNotFound {
		t.Fatalf("open unknown conn: %v", err)
	}
	e.tab = "nope"
	if _, err := e.run(ctx, "select 1", quarryv1.RunMode_RUN_MODE_STATEMENT, 0, 0, 0); code(err) != connect.CodeNotFound {
		t.Fatalf("run unknown tab: %v", err)
	}
	if _, err := e.s.Analyze(ctx, connect.NewRequest(&quarryv1.AnalyzeRequest{TabId: "nope"})); code(err) != connect.CodeNotFound {
		t.Fatalf("analyze unknown tab: %v", err)
	}
	if _, err := e.s.Cancel(ctx, connect.NewRequest(&quarryv1.CancelRequest{TabId: "nope"})); code(err) != connect.CodeNotFound {
		t.Fatalf("cancel unknown tab: %v", err)
	}

	e.tab = e.open(t, "off")
	if _, err := e.run(ctx, "select 1", quarryv1.RunMode_RUN_MODE_STATEMENT, 0, 0, 0); code(err) != connect.CodeFailedPrecondition || !errors.Is(err, service.ErrNotConnected) {
		t.Fatalf("run not connected: %v", err)
	}
	e.tab = e.open(t, "broken")
	if _, err := e.run(ctx, "select 1", quarryv1.RunMode_RUN_MODE_STATEMENT, 0, 0, 0); code(err) != connect.CodeUnavailable {
		t.Fatalf("run with failing session: %v", err)
	}
	if _, err := e.s.Rollback(ctx, connect.NewRequest(&quarryv1.RollbackRequest{TabId: e.tab})); code(err) != connect.CodeUnavailable {
		t.Fatalf("rollback with failing session: %v", err)
	}
}

func TestRebindClosesSession(t *testing.T) {
	e := newEnv(t, nil)
	other := newFakeConn(nil)
	e.live.add("other", driver.Postgres, other)
	e.runAll(t, "select 1", 0)
	res, err := e.s.Rebind(context.Background(), connect.NewRequest(&quarryv1.RebindRequest{
		TabId: e.tab, ConnId: "other", Scope: &quarryv1.Scope{Database: "db2", Schema: "s"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.Language != "sql" || !e.conn.session(0).closed.Load() {
		t.Fatalf("rebind = %v, old session closed %v", res.Msg, e.conn.session(0).closed.Load())
	}
	e.runAll(t, "select 1", 0)
	if other.count() != 1 || other.session(0).scope != (driver.Scope{Database: "db2", Schema: "s"}) || e.conn.count() != 1 {
		t.Fatal("run after rebind did not use the new connection and scope")
	}
}

func TestReopenSession(t *testing.T) {
	e := newEnv(t, nil)
	e.runAll(t, "select 1", 0)
	e.runAll(t, "select 1", 0)
	if e.conn.count() != 1 {
		t.Fatalf("sessions = %d", e.conn.count())
	}

	// The session died: a new one is opened on the same connection.
	e.conn.session(0).dead.Store(true)
	e.runAll(t, "select 1", 0)
	if e.conn.count() != 2 || !e.conn.session(0).closed.Load() {
		t.Fatalf("sessions = %d after a dead session", e.conn.count())
	}

	// The connection was replaced (reconnect): the session follows it.
	fresh := newFakeConn(nil)
	e.live.add("pg", driver.Postgres, fresh)
	e.runAll(t, "select 1", 0)
	if fresh.count() != 1 || !e.conn.session(1).closed.Load() {
		t.Fatal("run after reconnect did not open a session on the new connection")
	}
}

func TestTxState(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, map[string]fakeStmt{"select big": {cols: 1, rows: 5000}})
	evs := e.runAll(t, "select 1", 0)
	if tx := finished(t, evs).Tx; !tx.Supported || !tx.AutoCommit || tx.InTx {
		t.Fatalf("tx = %v", tx)
	}
	ac, err := e.s.SetAutoCommit(ctx, connect.NewRequest(&quarryv1.SetAutoCommitRequest{TabId: e.tab, On: false}))
	if err != nil {
		t.Fatal(err)
	}
	if tx := ac.Msg.Tx; !tx.Supported || tx.AutoCommit || tx.InTx {
		t.Fatalf("tx after SetAutoCommit = %v", tx)
	}
	evs = e.runAll(t, "insert x", 0)
	if tx := finished(t, evs).Tx; tx.AutoCommit || !tx.InTx {
		t.Fatalf("tx after insert = %v", tx)
	}
	e.runAll(t, "select big", 10)
	res := e.conn.session(0).cur
	rb, err := e.s.Rollback(ctx, connect.NewRequest(&quarryv1.RollbackRequest{TabId: e.tab}))
	if err != nil {
		t.Fatal(err)
	}
	if tx := rb.Msg.Tx; tx.AutoCommit || tx.InTx {
		t.Fatalf("tx after rollback = %v", tx)
	}
	if !res.closed.Load() {
		t.Fatal("rollback kept the open result")
	}
	cm, err := e.s.Commit(ctx, connect.NewRequest(&quarryv1.CommitRequest{TabId: e.tab}))
	if err != nil || !cm.Msg.Tx.Supported {
		t.Fatalf("commit = %v, %v", cm, err)
	}

	// A session without transactions, opened by the tx call itself.
	plain := newFakeConn(nil)
	plain.noTx = true
	e.live.add("plain", driver.Postgres, plain)
	e.tab = e.open(t, "plain")
	ac, err = e.s.SetAutoCommit(ctx, connect.NewRequest(&quarryv1.SetAutoCommitRequest{TabId: e.tab, On: false}))
	if err != nil || ac.Msg.Tx.Supported || plain.count() != 1 {
		t.Fatalf("SetAutoCommit on plain session = %v, %v", ac, err)
	}
	if tx := finished(t, e.runAll(t, "select 1", 0)).Tx; tx.Supported {
		t.Fatalf("tx = %v", tx)
	}
}

func TestRunWaitsForCanceledRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, map[string]fakeStmt{"select pg_sleep(30)": {blockRun: true}})
		ctx, cancel := context.WithCancel(context.Background())
		r := e.runBG(ctx, "select pg_sleep(30)")
		synctest.Wait()
		// The client goes away and immediately runs again: the new run waits
		// for the canceled one instead of failing with "tab is busy".
		cancel()
		evs := e.runAll(t, "select 1", 0)
		<-r.done
		if f := finished(t, evs); f.Canceled {
			t.Fatalf("second run: %v", kinds(evs))
		}
	})
}
