package export

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
)

var stmts = map[string]fakeStmt{
	"select x":       {cols: 2, rows: 3},
	"select big":     {cols: 2, rows: 2500},
	"select nothing": {cols: 0},
	"select broken":  {err: &driver.QueryError{Msg: "relation \"nope\" does not exist", Code: "42P01"}},
	"select down":    {err: errors.New("connection reset")},
	"select slow":    {cols: 2, rows: 20000, delay: 30 * time.Millisecond},
	"select stuck":   {cols: 2, rows: 5000, blockAt: 2500},
	"SELECT * FROM Shop.Orders WHERE (id > 1) ORDER BY id": {cols: 2, rows: 3},
}

type recorder struct {
	msgs []*quarryv1.ExportResponse
	at   []time.Time
	err  error // returned by send
}

func (r *recorder) send(m *quarryv1.ExportResponse) error {
	r.msgs = append(r.msgs, m)
	r.at = append(r.at, time.Now())
	return r.err
}

func sqlReq(path, sql string, o *quarryv1.ExportOptions) *quarryv1.ExportRequest {
	return &quarryv1.ExportRequest{ConnId: "pg", Path: path, Options: o, Source: &quarryv1.ExportRequest_Sql{Sql: sql}}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// entries lists dir, so tests can check that no partial file is left behind.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestCSV(t *testing.T) {
	c := newConn(stmts)
	s := New(&fakeLive{conn: c, kind: driver.Postgres})
	path := filepath.Join(t.TempDir(), "out.csv")
	req := sqlReq(path, "  select x;  ", &quarryv1.ExportOptions{Format: "csv", Header: true, Crlf: true, NullText: "NULL"})
	req.Scope = &quarryv1.Scope{Database: "app", Schema: "shop"}
	var r recorder
	if err := s.export(t.Context(), req, r.send); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, path), "id,name\r\n1,v1\r\n2,NULL\r\n3,v3\r\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if last := r.msgs[len(r.msgs)-1]; last.Rows != 3 || !last.Done {
		t.Fatalf("last message = %v", last)
	}
	if want := (driver.Scope{Database: "app", Schema: "shop"}); len(c.scopes) != 1 || c.scopes[0] != want {
		t.Fatalf("scopes = %v", c.scopes)
	}
	sess := c.sessions[0]
	if !sess.closed || !sess.res[0].closed {
		t.Fatal("session or result left open")
	}
	if !slices.Equal(c.executed, []string{"select x"}) {
		t.Fatalf("executed = %q", c.executed)
	}
}

func TestOptions(t *testing.T) {
	dir := t.TempDir()
	s := New(&fakeLive{conn: newConn(stmts), kind: driver.Postgres})
	tests := []struct {
		name string
		file string
		o    *quarryv1.ExportOptions
		want string
	}{
		{"defaults", "a.csv", nil, "id,name\r\n1,v1\r\n2,\r\n3,v3\r\n"},
		{"semicolon", "b.csv", &quarryv1.ExportOptions{Format: "CSV", Delimiter: ";", QuoteAll: true}, `"1";"v1"` + "\n" + `"2";` + "\n"},
		{"tsv", "c.txt", &quarryv1.ExportOptions{Format: "tsv", Header: true, Delimiter: ";"}, "id\tname\n1\tv1\n2\t\n3\tv3\n"},
		{"json by extension", "d.json", nil, "[\n"},
		{"ndjson", "e.out", &quarryv1.ExportOptions{Format: "ndjson"}, `{"id":1,"name":"v1"}` + "\n"},
		{"sql", "f.sql", &quarryv1.ExportOptions{Format: "sql"}, `INSERT INTO t (id, name) VALUES (1, 'v1');` + "\n"},
		{"sql batched into table", "g.sql", &quarryv1.ExportOptions{Format: "sql", Table: "app.Copy", BatchRows: 2}, "INSERT INTO app.Copy (id, name) VALUES\n  (1, 'v1'),\n  (2, NULL);\n"},
		{"markdown", "h.md", &quarryv1.ExportOptions{Format: "markdown", Header: true}, "|  id | name |\n| --: | ---- |\n|   1 | v1   |\n|   2 | NULL |\n"},
		{"bom", "i.csv", &quarryv1.ExportOptions{Format: "csv", Bom: true}, "\xef\xbb\xbf1,v1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.file)
			var r recorder
			if err := s.export(t.Context(), sqlReq(path, "select x", tt.o), r.send); err != nil {
				t.Fatal(err)
			}
			if got := read(t, path); !strings.HasPrefix(got, tt.want) {
				t.Fatalf("content = %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestDataSource(t *testing.T) {
	c := newConn(stmts)
	s := New(&fakeLive{conn: c, kind: driver.Postgres})
	dir := t.TempDir()
	data := &quarryv1.DataSource{
		Node:    &quarryv1.Node{Path: []string{"app", "Shop", "tables", "Orders"}, Kind: "table", Name: "Orders"},
		Filter:  "id > 1",
		OrderBy: []string{"id"},
	}
	req := func(path string, o *quarryv1.ExportOptions) *quarryv1.ExportRequest {
		return &quarryv1.ExportRequest{ConnId: "pg", Path: path, Options: o, Source: &quarryv1.ExportRequest_Data{Data: data}}
	}

	path := filepath.Join(dir, "orders.sql")
	if err := s.export(t.Context(), req(path, nil), new(recorder).send); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, path), `INSERT INTO "Shop"."Orders" (id, name) VALUES (1, 'v1');`; !strings.HasPrefix(got, want) {
		t.Fatalf("content = %q, want prefix %q", got, want)
	}
	if !slices.Equal(c.executed, []string{"SELECT * FROM Shop.Orders WHERE (id > 1) ORDER BY id"}) {
		t.Fatalf("executed = %q", c.executed)
	}

	path = filepath.Join(dir, "orders2.sql")
	if err := s.export(t.Context(), req(path, &quarryv1.ExportOptions{Format: "sql", Table: "archive.orders"}), new(recorder).send); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.HasPrefix(got, "INSERT INTO archive.orders (") {
		t.Fatalf("content = %q", got)
	}

	// A filter that smuggles a second statement in is refused.
	data.Filter = "true); delete from orders; select (1"
	if err := s.export(t.Context(), req(filepath.Join(dir, "x.sql"), nil), new(recorder).send); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("injected filter: %v", err)
	}

	plain := New(&fakeLive{conn: plainConn{c}, kind: driver.Postgres})
	if err := plain.export(t.Context(), req(filepath.Join(dir, "y.sql"), nil), new(recorder).send); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("no data viewer: %v", err)
	}
}

func TestRejects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.csv")
	tests := []struct {
		name string
		live *fakeLive
		req  *quarryv1.ExportRequest
		code connect.Code
		msg  string
	}{
		{"relative path", nil, sqlReq("out.csv", "select x", nil), connect.CodeInvalidArgument, "absolute"},
		{"unknown connection", nil, &quarryv1.ExportRequest{ConnId: "nope", Path: path, Source: &quarryv1.ExportRequest_Sql{Sql: "select x"}}, connect.CodeNotFound, ""},
		{"not connected", &fakeLive{kind: driver.Postgres}, sqlReq(path, "select x", nil), connect.CodeFailedPrecondition, ""},
		{"no source", nil, &quarryv1.ExportRequest{ConnId: "pg", Path: path}, connect.CodeInvalidArgument, "statement or a data source"},
		{"empty", nil, sqlReq(path, " -- nothing\n", nil), connect.CodeInvalidArgument, "no statement"},
		{"several statements", nil, sqlReq(path, "select x; select x", nil), connect.CodeInvalidArgument, "exactly one statement, got 2"},
		{"write", nil, sqlReq(path, "delete from t where id = 1", nil), connect.CodeInvalidArgument, "do not modify data"},
		{"write in cte", nil, sqlReq(path, "with d as (delete from t returning *) select * from d", nil), connect.CodeInvalidArgument, "do not modify data"},
		{"unknown format", nil, sqlReq(path, "select x", &quarryv1.ExportOptions{Format: "xlsx"}), connect.CodeInvalidArgument, "xlsx"},
		{"long delimiter", nil, sqlReq(path, "select x", &quarryv1.ExportOptions{Format: "csv", Delimiter: ";;"}), connect.CodeInvalidArgument, "delimiter"},
		{"quote delimiter", nil, sqlReq(path, "select x", &quarryv1.ExportOptions{Format: "csv", Delimiter: `"`}), connect.CodeInvalidArgument, "delimiter"},
		{"no rows", nil, sqlReq(path, "select nothing", nil), connect.CodeInvalidArgument, "returns no rows"},
		{"query error", nil, sqlReq(path, "select broken", nil), connect.CodeInvalidArgument, `relation "nope" does not exist`},
		{"driver error", nil, sqlReq(path, "select down", nil), connect.CodeUnavailable, "connection reset"},
		{"missing directory", nil, sqlReq(filepath.Join(dir, "missing", "out.csv"), "select x", nil), connect.CodeInvalidArgument, "does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := tt.live
			if live == nil {
				live = &fakeLive{conn: newConn(stmts), kind: driver.Postgres}
			}
			var r recorder
			err := New(live).export(t.Context(), tt.req, r.send)
			if connect.CodeOf(err) != tt.code || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("err = %v, want %v containing %q", err, tt.code, tt.msg)
			}
			if len(r.msgs) != 0 {
				t.Fatalf("sent %v", r.msgs)
			}
			if es := entries(t, dir); len(es) != 0 {
				t.Fatalf("left %v", es)
			}
		})
	}
}

func TestProgressThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(&fakeLive{conn: newConn(stmts), kind: driver.Postgres})
		path := filepath.Join(t.TempDir(), "slow.ndjson")
		var r recorder
		start := time.Now()
		if err := s.export(t.Context(), sqlReq(path, "select slow", &quarryv1.ExportOptions{Format: "ndjson"}), r.send); err != nil {
			t.Fatal(err)
		}
		// Started, then a page every 30 ms reported at most every 100 ms, then done.
		var rows []int64
		for _, m := range r.msgs {
			rows = append(rows, m.Rows)
		}
		if want := []int64{0, 4000, 8000, 12000, 16000, 20000, 20000}; !slices.Equal(rows, want) {
			t.Fatalf("rows = %v, want %v", rows, want)
		}
		if r.at[0] != start || !r.msgs[len(r.msgs)-1].Done {
			t.Fatalf("messages = %v", r.msgs)
		}
		for i := 1; i < len(r.msgs)-1; i++ {
			if gap := r.at[i].Sub(r.at[i-1]); gap < progressEvery || r.msgs[i].Done {
				t.Fatalf("message %d = %v after %v", i, r.msgs[i], gap)
			}
		}
	})
}

func TestCancel(t *testing.T) {
	dir := t.TempDir()
	c := newConn(stmts)
	s := New(&fakeLive{conn: c, kind: driver.Postgres})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() {
		done <- s.export(ctx, sqlReq(filepath.Join(dir, "out.csv"), "select stuck", nil), new(recorder).send)
	}()
	<-c.reached
	if es := entries(t, dir); len(es) != 1 || !strings.HasSuffix(es[0], ".tmp") {
		t.Fatalf("while writing: %v", es)
	}
	cancel()
	if err := <-done; connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("err = %v", err)
	}
	if es := entries(t, dir); len(es) != 0 {
		t.Fatalf("left %v", es)
	}
}

func TestSendFailureStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		s := New(&fakeLive{conn: newConn(stmts), kind: driver.Postgres})
		gone := errors.New("client gone")
		r := recorder{err: gone}
		err := s.export(t.Context(), sqlReq(filepath.Join(dir, "out.csv"), "select slow", nil), r.send)
		if !errors.Is(err, gone) || len(r.msgs) != 1 {
			t.Fatalf("err = %v after %d messages", err, len(r.msgs))
		}
		if es := entries(t, dir); len(es) != 0 {
			t.Fatalf("left %v", es)
		}
	})
}

func newClient(t *testing.T, s *Service) quarryv1connect.ExportServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewExportServiceHandler(s))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return quarryv1connect.NewExportServiceClient(srv.Client(), srv.URL)
}

func TestConnect(t *testing.T) {
	dir := t.TempDir()
	c := newConn(stmts)
	client := newClient(t, New(&fakeLive{conn: c, kind: driver.Postgres}))

	path := filepath.Join(dir, "big.csv")
	stream, err := client.Export(t.Context(), connect.NewRequest(sqlReq(path, "select big", nil)))
	if err != nil {
		t.Fatal(err)
	}
	var last *quarryv1.ExportResponse
	for stream.Receive() {
		last = stream.Msg()
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if last == nil || last.Rows != 2500 || !last.Done {
		t.Fatalf("last = %v", last)
	}
	if got := strings.Count(read(t, path), "\n"); got != 2501 {
		t.Fatalf("%d lines", got)
	}

	// Closing the stream cancels the export and removes the partial file.
	c = newConn(stmts)
	client = newClient(t, New(&fakeLive{conn: c, kind: driver.Postgres}))
	ctx, cancel := context.WithCancel(t.Context())
	stream, err = client.Export(ctx, connect.NewRequest(sqlReq(filepath.Join(dir, "stuck.csv"), "select stuck", nil)))
	if err != nil {
		t.Fatal(err)
	}
	<-c.reached
	cancel()
	stream.Close()
	select {
	case <-c.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("export kept running after the stream closed")
	}
	if es := entries(t, dir); !slices.Equal(es, []string{"big.csv"}) {
		t.Fatalf("dir = %v", es)
	}
}

func TestParams(t *testing.T) {
	c := newConn(map[string]fakeStmt{"select x where id = 7 and name = 'o''k'": {cols: 2, rows: 1}})
	s := New(&fakeLive{conn: c, kind: driver.Postgres})
	path := filepath.Join(t.TempDir(), "out.csv")
	req := sqlReq(path, "select x where id = :id and name = $1", &quarryv1.ExportOptions{Format: "csv"})
	req.Params = map[string]string{"id": "7", "1": "o'k"}
	var r recorder
	if err := s.export(t.Context(), req, r.send); err != nil {
		t.Fatal(err)
	}
	if want := "select x where id = 7 and name = 'o''k'"; !slices.Equal(c.executed, []string{want}) {
		t.Fatalf("executed = %q, want %q", c.executed, want)
	}

	req.Params = map[string]string{"id": "7"}
	err := s.export(t.Context(), req, r.send)
	if ce, ok := errors.AsType[*connect.Error](err); !ok || ce.Code() != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "$1") {
		t.Fatalf("missing value: %v", err)
	}
}
