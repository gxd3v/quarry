package query

import (
	"context"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/postgres"
	"github.com/gxd3v/quarry/service"
	"github.com/gxd3v/quarry/service/catalog"
)

// Integration tests run against the server named by QUARRY_TEST_PG_DSN, e.g.
// postgres://postgres:quarry@localhost:55432/postgres?sslmode=disable

type pgLive struct {
	conn driver.Conn
	cfg  driver.Config
}

func (l *pgLive) Live(string) (driver.Conn, driver.Config, error) { return l.conn, l.cfg, nil }
func (l *pgLive) Config(string) (driver.Config, bool)             { return l.cfg, true }

func newPGEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("QUARRY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_PG_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	cfg := driver.Config{
		ID:       uuid.New(),
		Kind:     driver.Postgres,
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"),
		Options:  map[string]string{},
	}
	for k, v := range u.Query() {
		cfg.Options[k] = v[0]
	}
	conn, err := postgres.New().Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	e := &env{hist: &memHistory{}}
	e.s = New(&pgLive{conn: conn, cfg: cfg}, WithLanguage(sqlLang), WithHistory(e.hist))
	t.Cleanup(e.s.Close)
	e.tab = e.open(t, "pg")
	return e
}

func TestITStreamAndFetch(t *testing.T) {
	e := newPGEnv(t)
	evs := e.runAll(t, "select generate_series(1, 50000) as n", 0)
	if got := batches(evs); !slices.Equal(got, []int{200, 2000, 2000, 2000, 2000, 1800}) {
		t.Fatalf("batches = %v", got)
	}
	if c := evs[2].GetColumns().Columns; len(c) != 1 || c[0].Name != "n" || c[0].Type != "int4" {
		t.Fatalf("columns = %v", c)
	}
	if v := evs[3].GetRows().Rows[0].Values[0].GetInt(); v != 1 {
		t.Fatalf("first value = %d", v)
	}
	if d := dones(evs)[0]; d.Rows != 10000 || !d.HasMore {
		t.Fatalf("done = %v", d)
	}

	evs, err := e.fetch(t, -1)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range batches(evs) {
		total += n
	}
	d := dones(evs)[0]
	if total != 40000 || d.Rows != 50000 || d.HasMore || d.Tag != "SELECT 50000" {
		t.Fatalf("fetch: %d rows, done = %v", total, d)
	}
	if v := evs[0].GetRows().Rows[0].Values[0].GetInt(); v != 10001 {
		t.Fatalf("fetch resumed at %d", v)
	}
}

func TestITCancel(t *testing.T) {
	e := newPGEnv(t)
	started := make(chan struct{})
	var evs []*quarryv1.RunEvent
	done := make(chan error)
	go func() {
		done <- e.s.run(context.Background(), &quarryv1.RunRequest{TabId: e.tab, Script: "select pg_sleep(30)", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT},
			func(ev *quarryv1.RunEvent) error {
				evs = append(evs, ev)
				if ev.GetStatement() != nil {
					close(started)
				}
				return nil
			})
	}()
	<-started
	time.Sleep(200 * time.Millisecond)
	began := time.Now()
	e.cancel(t)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop pg_sleep")
	}
	if f := finished(t, evs); !f.Canceled || time.Since(began) > 5*time.Second {
		t.Fatalf("finished = %v after %v", f, time.Since(began))
	}

	evs = e.runAll(t, "select 42", 0)
	if errorEvent(evs) != nil || evs[3].GetRows().Rows[0].Values[0].GetInt() != 42 {
		t.Fatalf("run after cancel = %v", evs)
	}
}

func TestITErrorPositionAndMessages(t *testing.T) {
	e := newPGEnv(t)
	script := "-- ação 😀 ünïcode\nselect 1; select nosuchcol from pg_class;\nselect 2;"
	evs := e.runAll(t, script, 0)
	if got := kinds(evs); !slices.Equal(got, []string{"started", "statement", "columns", "rows", "done", "statement", "error", "finished"}) {
		t.Fatalf("events = %v", got)
	}
	qe := errorEvent(evs)
	want := service.UTF16Offset(script, strings.Index(script, "nosuchcol"))
	if qe.Code != "42703" || int(qe.Position) != want {
		t.Fatalf("error = %v, want position %d", qe, want)
	}

	evs = e.runAll(t, "do $$ begin raise notice 'héllo'; end $$", 0)
	if got := kinds(evs); !slices.Equal(got, []string{"started", "statement", "done", "message", "finished"}) {
		t.Fatalf("events = %v", got)
	}
	if m := evs[3].GetMessage(); m.Severity != "NOTICE" || m.Text != "héllo" {
		t.Fatalf("message = %v", m)
	}
}

func TestITManualCommit(t *testing.T) {
	e := newPGEnv(t)
	ctx := context.Background()
	e.runAll(t, "create temp table qt (id int)", 0)
	if _, err := e.s.SetAutoCommit(ctx, connect.NewRequest(&quarryv1.SetAutoCommitRequest{TabId: e.tab, On: false})); err != nil {
		t.Fatal(err)
	}
	evs := e.runAll(t, "insert into qt values (1), (2)", 0)
	if d := dones(evs)[0]; d.Affected != 2 || d.Tag != "INSERT 0 2" {
		t.Fatalf("done = %v", d)
	}
	if tx := finished(t, evs).Tx; !tx.Supported || tx.AutoCommit || !tx.InTx {
		t.Fatalf("tx = %v", tx)
	}
	rb, err := e.s.Rollback(ctx, connect.NewRequest(&quarryv1.RollbackRequest{TabId: e.tab}))
	if err != nil {
		t.Fatal(err)
	}
	if rb.Msg.Tx.InTx {
		t.Fatalf("tx after rollback = %v", rb.Msg.Tx)
	}
	evs = e.runAll(t, "select count(*) from qt", 0)
	if n := evs[3].GetRows().Rows[0].Values[0].GetInt(); n != 0 {
		t.Fatalf("rows after rollback = %d", n)
	}
	cm, err := e.s.Commit(ctx, connect.NewRequest(&quarryv1.CommitRequest{TabId: e.tab}))
	if err != nil || cm.Msg.Tx.InTx {
		t.Fatalf("commit = %v, %v", cm, err)
	}
	ac, err := e.s.SetAutoCommit(ctx, connect.NewRequest(&quarryv1.SetAutoCommitRequest{TabId: e.tab, On: true}))
	if err != nil || !ac.Msg.Tx.AutoCommit || ac.Msg.Tx.InTx {
		t.Fatalf("autocommit on = %v, %v", ac, err)
	}
}

func TestITComplete(t *testing.T) {
	e := newPGEnv(t)
	e.s.catalog = catalog.New(e.s.live)
	schema := "quarry_complete_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	e.runAll(t, "create schema "+schema+"; create table "+schema+".items (id int primary key, label text)", 0)
	t.Cleanup(func() { e.runAll(t, "drop schema "+schema+" cascade", 0) })

	r, _ := e.completeAt(t, e.tab, "select * from "+schema+".|")
	if it := item(r, "items"); it == nil || it.Kind != "table" {
		t.Fatalf("tables: %v", itemLabels(r))
	}
	r, _ = e.completeAt(t, e.tab, "select i.la| from "+schema+".items i")
	if it := item(r, "label"); it == nil || it.Kind != "column" || len(r.Items) != 1 {
		t.Fatalf("columns: %v", itemLabels(r))
	}
}
