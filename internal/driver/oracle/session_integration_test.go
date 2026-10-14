package oracle

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

func exec(t *testing.T, s driver.Session, text string) driver.Result {
	t.Helper()
	r, err := s.Execute(context.Background(), driver.Statement{Text: text})
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return r
}

func tag(r driver.Result) string { return r.(interface{ Tag() string }).Tag() }

func TestITDMLAndDDL(t *testing.T) {
	c := openUser(t)
	s := newSession(t, c, driver.Scope{})
	ctx := context.Background()
	r := exec(t, s, "create table dml (id number primary key, v varchar2(10));")
	if r.Affected() != -1 || len(r.Columns()) != 0 || tag(r) != "CREATE TABLE" {
		t.Errorf("DDL: affected %d, cols %v, tag %q", r.Affected(), r.Columns(), tag(r))
	}
	if rows, err := r.Next(ctx, 10); err != nil || len(rows) != 0 {
		t.Errorf("DDL Next: %v, %v", rows, err)
	}
	r.Close()
	r.Close()
	if r := exec(t, s, "insert into dml select level, 'x' from dual connect by level <= 3"); r.Affected() != 3 || tag(r) != "INSERT 3" {
		t.Errorf("insert: affected %d, tag %q", r.Affected(), tag(r))
	}
	if r := exec(t, s, "update dml set v = 'y' where id > 1"); r.Affected() != 2 {
		t.Errorf("update affected %d", r.Affected())
	}
	if r := exec(t, s, "merge into dml d using (select 1 id from dual) s on (d.id = s.id) when matched then update set v = 'm'"); r.Affected() != 1 {
		t.Errorf("merge affected %d", r.Affected())
	}
	r, rows := all(t, s, "select id, v from dml order by id")
	if !reflect.DeepEqual(rows, [][]any{{int64(1), "m"}, {int64(2), "y"}, {int64(3), "y"}}) || tag(r) != "SELECT 3" {
		t.Errorf("select: %#v, tag %q", rows, tag(r))
	}
	if r := exec(t, s, "begin delete from dml where id = 1; end;"); r.Affected() != -1 || tag(r) != "PL/SQL BLOCK" {
		t.Errorf("block: affected %d, tag %q", r.Affected(), tag(r))
	}
	if r := exec(t, s, "drop table dml purge"); r.Affected() != -1 || tag(r) != "DROP TABLE" {
		t.Errorf("drop: %d %q", r.Affected(), tag(r))
	}
	if r, err := s.Execute(ctx, driver.Statement{Text: "-- nothing"}); err != nil || r.Affected() != -1 {
		t.Errorf("comment-only: %v", err)
	}
	// A whole statement as typed, with its terminator.
	if _, rows := all(t, s, "select 1 from dual;\n/\n"); rows[0][0] != int64(1) {
		t.Errorf("terminated statement: %#v", rows)
	}
}

func TestITErrorPosition(t *testing.T) {
	c := openUser(t)
	s := newSession(t, c, driver.Scope{})
	for _, text := range []string{
		"select 1 from nosuchtable",
		"select 'ção', '€' x,\n  nosuchtable.x from dual",
	} {
		_, err := s.Execute(context.Background(), driver.Statement{Text: text})
		qe, ok := errors.AsType[*driver.QueryError](err)
		if !ok {
			t.Fatalf("%s: got %T %v", text, err, err)
		}
		want := strings.Index(text, "nosuchtable")
		if qe.Position != want || !strings.HasPrefix(qe.Code, "ORA-") || strings.Contains(qe.Msg, "ORA-") {
			t.Errorf("%s: position %d (code %s), want %d: %v", text, qe.Position, qe.Code, want, qe)
		}
	}
	_, err := s.Execute(context.Background(), driver.Statement{Text: "select 1/0 from dual"})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Code != "ORA-01476" || qe.Msg != "divisor is equal to zero" {
		t.Errorf("division by zero: %#v", err)
	}
	_, err = s.Execute(context.Background(), driver.Statement{Text: "begin raise_application_error(-20001, 'boom'); end;"})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Code != "ORA-20001" || qe.Msg != "boom" ||
		!strings.Contains(qe.Detail, "ORA-06512") || qe.Position != -1 {
		t.Errorf("raise: %#v", err)
	}
	all(t, s, "select 1 from dual") // session still usable
}

func TestITCompileErrors(t *testing.T) {
	c := openUser(t)
	s := newSession(t, c, driver.Scope{})
	text := "create or replace procedure bad_proc as\nbegin\n  nosuch;\nend;"
	_, err := s.Execute(context.Background(), driver.Statement{Text: text})
	qe, ok := errors.AsType[*driver.QueryError](err)
	if !ok || qe.Code != "PLS-00201" || qe.Position != strings.Index(text, "nosuch") ||
		!strings.Contains(qe.Detail, "procedure BAD_PROC was created with compilation errors") {
		t.Fatalf("compile error: %#v", err)
	}
	trg := "create or replace trigger bad_trg before insert on customers for each row\nbegin\n  :new.nosuch := 1;\nend;"
	_, err = s.Execute(context.Background(), driver.Statement{Text: trg})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Position != strings.Index(trg, ":new.nosuch") && qe.Position != strings.Index(trg, "nosuch") {
		t.Errorf("trigger compile error: %#v (want near %d)", err, strings.Index(trg, ":new.nosuch"))
	}
	exec(t, s, "drop trigger bad_trg")
	exec(t, s, "create or replace procedure bad_proc as\nbegin\n  null;\nend;\n/")
	exec(t, s, "drop procedure bad_proc")
}

func TestITOutput(t *testing.T) {
	c := openUser(t)
	s := newSession(t, c, driver.Scope{})
	ms := s.(driver.MessageSource)
	exec(t, s, "begin dbms_output.put_line('hello'); dbms_output.put_line(null); dbms_output.put_line('wörld'); end;")
	want := []driver.Message{{Severity: "OUTPUT", Text: "hello"}, {Severity: "OUTPUT", Text: ""}, {Severity: "OUTPUT", Text: "wörld"}}
	if got := ms.DrainMessages(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := ms.DrainMessages(); len(got) != 0 {
		t.Errorf("second drain: %+v", got)
	}

	exec(t, s, "begin for i in 1 .. 2500 loop dbms_output.put_line('line ' || i); end loop; end;")
	if got := ms.DrainMessages(); len(got) != 2500 || got[2499].Text != "line 2500" {
		t.Errorf("batched output: %d lines", len(got))
	}
	exec(t, s, "begin for i in 1 .. 12000 loop dbms_output.put_line('x'); end loop; end;")
	got := ms.DrainMessages()
	if len(got) != maxOutputLines+1 || got[len(got)-1].Severity != "INFO" {
		t.Errorf("truncated output: %d messages, last %+v", len(got), got[len(got)-1])
	}

	// Output raised while a query runs, and errors after output.
	all(t, s, "select add_num(1, 2) from dual")
	_, err := s.Execute(context.Background(), driver.Statement{Text: "begin dbms_output.put_line('before'); raise zero_divide; end;"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := ms.DrainMessages(); len(got) != 1 || got[0].Text != "before" {
		t.Errorf("output before error: %+v", got)
	}

	// SQL*Plus commands.
	exec(t, s, "set serveroutput off")
	exec(t, s, "begin dbms_output.put_line('hidden'); end;")
	exec(t, s, "SET SERVEROUTPUT ON SIZE UNLIMITED")
	exec(t, s, "prompt hi there;")
	exec(t, s, "exec dbms_output.put_line('from exec');")
	exec(t, s, "set linesize 200")
	want = []driver.Message{
		{Severity: "OUTPUT", Text: "hi there"},
		{Severity: "OUTPUT", Text: "from exec"},
		{Severity: "INFO", Text: "SQL*Plus command ignored: set linesize 200"},
	}
	if got := ms.DrainMessages(); !reflect.DeepEqual(got, want) {
		t.Errorf("sqlplus: got %+v, want %+v", got, want)
	}
	text := "exec nosuch_proc(1)"
	_, err = s.Execute(context.Background(), driver.Statement{Text: text})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Position != strings.Index(text, "nosuch_proc") {
		t.Errorf("exec error position: %#v", err)
	}
	if _, err := s.Execute(context.Background(), driver.Statement{Text: "connect scott/tiger"}); err == nil {
		t.Error("CONNECT should fail")
	}
	r, rows := all(t, s, "desc orders")
	if len(r.Columns()) != 3 || !reflect.DeepEqual(rows[1], []any{"CUSTOMER_ID", "NOT NULL", "NUMBER(10)"}) {
		t.Errorf("describe: %#v", rows)
	}
	if _, err := s.Execute(context.Background(), driver.Statement{Text: "desc nosuch"}); err == nil {
		t.Error("describe of a missing object should fail")
	}
}

func TestITCancel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		privileged bool
	}{{"privileged", true}, {"unprivileged", false}} {
		t.Run(tc.name, func(t *testing.T) {
			c := openUser(t)
			if tc.privileged {
				c = open(t)
			}
			s := newSession(t, c, driver.Scope{Schema: itUser})
			if got := s.(*session).ident != ""; got != tc.privileged {
				t.Fatalf("can cancel = %v", got)
			}

			// CPU-bound work stops on go-ora's in-band break.
			cancelled(t, s, "select count(*) from all_objects a, all_objects b, all_objects c", 3*time.Second)
			if _, rows := all(t, s, "select 7 from dual"); rows[0][0] != int64(7) {
				t.Errorf("after cancel: %#v", rows)
			}

			if !tc.privileged {
				// A wait ignores the break; the session is dropped after the
				// cancel deadline.
				err := cancelled(t, s, "begin dbms_session.sleep(30); end;", cancelDeadline+3*time.Second)
				if s.(AliveChecker).Alive() || !strings.Contains(err.Error(), "disconnected") {
					t.Errorf("after an ignored cancel: alive %v, %v", s.(AliveChecker).Alive(), err)
				}
				if _, err := s.Execute(context.Background(), driver.Statement{Text: "select 1 from dual"}); !errors.Is(err, errLost) {
					t.Errorf("dead session: %v", err)
				}
				return
			}
			// Waits stop through ALTER SYSTEM CANCEL SQL.
			cancelled(t, s, "begin dbms_session.sleep(30); end;", 3*time.Second)
			// Recursive SQL is cancelled untargeted; the leftover interrupt
			// must not hit the next statement.
			cancelled(t, s, "begin execute immediate 'begin dbms_session.sleep(30); end;'; end;", 3*time.Second)
			for range 3 {
				all(t, s, "select 1 from dual")
			}

			// Cancelling while rows are fetched.
			ctx := context.Background()
			r, err := s.Execute(ctx, driver.Statement{Text: "select slow_row(level) from dual connect by level <= 20000"})
			if err != nil {
				t.Fatal(err)
			}
			nctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			start := time.Now()
			read := 0
			for {
				page, err := r.Next(nctx, 1000)
				read += len(page)
				if err != nil {
					t.Logf("fetch cancelled after %d rows in %v: %v", read, time.Since(start), err)
					break
				}
				if len(page) == 0 {
					t.Fatal("result was not cancelled")
				}
			}
			if took := time.Since(start); took > 4*time.Second {
				t.Errorf("fetch cancel took %v", took)
			}
			r.Close()
			if _, rows := all(t, s, "select 8 from dual"); rows[0][0] != int64(8) {
				t.Errorf("after fetch cancel: %#v", rows)
			}

			// Cancelling the Execute context after it returned does not
			// affect paging.
			ctx2, cancel2 := context.WithCancel(ctx)
			r, err = s.Execute(ctx2, driver.Statement{Text: "select level from dual connect by level <= 5000"})
			cancel2()
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for {
				page, err := r.Next(ctx, 1000)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				n += len(page)
			}
			if n != 5000 {
				t.Errorf("read %d rows", n)
			}
		})
	}
}

// cancelled runs text with a context cancelled after 300ms and checks that
// it fails within max.
func cancelled(t *testing.T, s driver.Session, text string, max time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Execute(ctx, driver.Statement{Text: text})
	took := time.Since(start)
	if err == nil {
		t.Fatalf("%s: expected cancellation", text)
	}
	if took > max {
		t.Errorf("%s: cancel took %v", text, took)
	}
	t.Logf("%s: cancelled after %v: %v", text, took, err)
	return err
}

func TestITTransactions(t *testing.T) {
	c := openUser(t)
	s := newSession(t, c, driver.Scope{})
	other := newSession(t, c, driver.Scope{})
	tx := s.(driver.Transactional)
	ctx := context.Background()
	count := func(sess driver.Session) int64 {
		_, rows := all(t, sess, "select count(*) from customers")
		return rows[0][0].(int64)
	}
	before := count(s)

	// Autocommit: DML, CALL and PL/SQL are committed at once.
	exec(t, s, "insert into customers (name) values ('auto')")
	if tx.InTx() || count(other) != before+1 {
		t.Errorf("autocommit insert: in tx %v, other sees %d", tx.InTx(), count(other))
	}
	exec(t, s, "delete from customers where name = 'auto'")

	if err := tx.SetAutoCommit(ctx, false); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "insert into customers (name) values ('tmp')")
	if !tx.InTx() {
		t.Fatal("manual mode should keep the transaction open")
	}
	if count(s) != before+1 || count(other) != before {
		t.Errorf("inside tx: %d / %d", count(s), count(other))
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if tx.InTx() || count(s) != before {
		t.Errorf("after rollback: in tx %v, %d", tx.InTx(), count(s))
	}
	if all(t, s, "select 1 from dual"); tx.InTx() {
		t.Error("a query opened a transaction")
	}

	exec(t, s, "insert into customers (name) values ('kept')")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if tx.InTx() || count(other) != before+1 {
		t.Errorf("commit not visible: %d", count(other))
	}

	// DDL commits implicitly; a failed statement keeps earlier changes.
	exec(t, s, "delete from customers where name = 'kept'")
	exec(t, s, "create table tx_ddl (a number)")
	if tx.InTx() || count(other) != before {
		t.Errorf("DDL should commit: in tx %v, %d", tx.InTx(), count(other))
	}
	exec(t, s, "drop table tx_ddl purge")
	exec(t, s, "insert into customers (name) values ('x')")
	if _, err := s.Execute(ctx, driver.Statement{Text: "insert into customers (id, name) values (1, 'dup')"}); err == nil {
		t.Fatal("expected a unique violation")
	}
	if !tx.InTx() || count(s) != before+1 {
		t.Errorf("statement-level rollback: in tx %v, %d", tx.InTx(), count(s))
	}
	exec(t, s, "rollback")
	if tx.InTx() {
		t.Error("ROLLBACK statement should end the transaction")
	}

	// SELECT FOR UPDATE holds locks; in autocommit mode they are released
	// once all rows were read.
	exec(t, s, "select * from customers for update")
	if !tx.InTx() {
		t.Error("FOR UPDATE should open a transaction")
	}
	tx.Rollback(ctx)
	if err := tx.SetAutoCommit(ctx, true); err != nil {
		t.Fatal(err)
	}
	r, rows := all(t, s, "select id from customers for update")
	if len(rows) != int(before) || tx.InTx() {
		t.Errorf("autocommit FOR UPDATE: %d rows, in tx %v (%v)", len(rows), tx.InTx(), r.Columns())
	}

	// Switching autocommit back on commits.
	tx.SetAutoCommit(ctx, false)
	exec(t, s, "insert into customers (name) values ('on')")
	tx.SetAutoCommit(ctx, true)
	if tx.InTx() || count(other) != before+1 {
		t.Errorf("SetAutoCommit(true) should commit: %d", count(other))
	}
	exec(t, s, "delete from customers where name = 'on'")

	// Closing a session rolls back.
	s2 := newSession(t, c, driver.Scope{})
	s2.(driver.Transactional).SetAutoCommit(ctx, false)
	exec(t, s2, "insert into customers (name) values ('lost')")
	s2.Close()
	if count(other) != before {
		t.Errorf("Close should roll back: %d", count(other))
	}
}

func TestITPaging(t *testing.T) {
	c := openUser(t)
	s := newSession(t, c, driver.Scope{})
	ctx := context.Background()
	r, err := s.Execute(ctx, driver.Statement{Text: "select level, to_char(level) from dual connect by level <= 100000"})
	if err != nil {
		t.Fatal(err)
	}
	next := int64(1)
	for {
		page, err := r.Next(ctx, 999)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 999 {
			t.Fatalf("page of %d rows", len(page))
		}
		for _, row := range page {
			if row[0] != next || row[1] != strconv.FormatInt(next, 10) {
				t.Fatalf("row %d: %#v", next, row)
			}
			next++
		}
	}
	if next != 100001 {
		t.Errorf("read %d rows", next-1)
	}
	if page, err := r.Next(ctx, 10); len(page) != 0 || err != nil {
		t.Errorf("after exhaustion: %v %v", page, err)
	}
	r.Close()
	r.Close()

	r, err = s.Execute(ctx, driver.Statement{Text: "select level from dual connect by level <= 100000000"})
	if err != nil {
		t.Fatal(err)
	}
	r.Next(ctx, 10)
	start := time.Now()
	r.Close()
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("early close took %v", took)
	}
	all(t, s, "select 1 from dual")

	// Executing again closes a result that was left open.
	if _, err := s.Execute(ctx, driver.Statement{Text: "select level from dual connect by level <= 10"}); err != nil {
		t.Fatal(err)
	}
	all(t, s, "select 2 from dual")
}

func TestITReadOnly(t *testing.T) {
	cfg, pw := userConfig(t)
	cfg.ReadOnly = true
	c := openWith(t, cfg, pw)
	s := newSession(t, c, driver.Scope{})
	all(t, s, "select count(*) from customers")
	exec(t, s, "alter session set nls_date_format = 'YYYY-MM-DD'")
	for _, text := range []string{"insert into customers (name) values ('x')", "drop table customers", "begin null; end;", "exec null"} {
		if _, err := s.Execute(context.Background(), driver.Statement{Text: text}); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s: %v", text, err)
		}
	}
	err := c.(driver.Editable).Apply(context.Background(), &driver.Node{Path: []string{itUser, "tables", "NOKEY"}},
		[]driver.RowChange{{Op: driver.OpDelete, Key: map[string]any{"ROWID": "x"}}})
	if !errors.Is(err, errReadOnly) {
		t.Errorf("Apply: %v", err)
	}
}

func TestITHealth(t *testing.T) {
	c := openUser(t)
	p := c.(Pinger)
	if err := p.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, c, driver.Scope{})
	if !s.(AliveChecker).Alive() {
		t.Fatal("new session not alive")
	}
	// Kill the session and the metadata session from outside.
	withRaw(t, func(l *link) {
		var ids []string
		l.query(context.Background(), `SELECT sid || ',' || serial# FROM v$session WHERE username = :1`, []any{itUser},
			func(r []sqldriver.Value) error { ids = append(ids, str(r[0])); return nil })
		for _, id := range ids {
			l.exec(context.Background(), "ALTER SYSTEM KILL SESSION '"+id+"' IMMEDIATE")
		}
	})
	if _, err := s.Execute(context.Background(), driver.Statement{Text: "select 1 from dual"}); err == nil {
		t.Error("killed session should fail")
	}
	if s.(AliveChecker).Alive() {
		t.Error("killed session still alive")
	}
	err := p.Ping(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "connection lost") {
		t.Errorf("Ping after kill: %v", err)
	}
	if err := p.Ping(context.Background()); err != nil {
		t.Errorf("Ping should reconnect: %v", err)
	}
	c.Close()
	if err := p.Ping(context.Background()); !errors.Is(err, errClosed) {
		t.Errorf("Ping after Close: %v", err)
	}
}
