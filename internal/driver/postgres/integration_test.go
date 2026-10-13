package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

// Integration tests run against a live server named by QUARRY_TEST_PG_DSN, e.g.
// postgres://postgres:quarry@localhost:55432/postgres?sslmode=disable
// They create (and recreate) the database quarry_it.

const itDB = "quarry_it"

const fixture = `
CREATE SCHEMA shop;
CREATE TYPE shop.mood AS ENUM ('happy', 'sad');
CREATE TABLE shop.customers (
	id serial PRIMARY KEY,
	name text NOT NULL,
	email varchar(100) UNIQUE
);
CREATE TABLE shop.orders (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	customer_id int NOT NULL REFERENCES shop.customers (id),
	total numeric(12,2) CHECK (total >= 0),
	status text DEFAULT 'new'
);
CREATE INDEX orders_customer_idx ON shop.orders (customer_id);
CREATE VIEW shop.big_orders AS SELECT * FROM shop.orders WHERE total > 100;
CREATE MATERIALIZED VIEW shop.order_totals AS SELECT customer_id, sum(total) AS total FROM shop.orders GROUP BY 1;
CREATE FUNCTION shop.add(a int, b int) RETURNS int LANGUAGE sql AS 'select a + b';
CREATE SEQUENCE shop.invoice_seq;
INSERT INTO shop.customers (name) VALUES ('ann'), ('bob');
ANALYZE;
`

var (
	setupOnce sync.Once
	setupErr  error
)

func itConfig(t testing.TB) (driver.Config, string) {
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
	setupOnce.Do(func() { setupErr = setup(dsn) })
	if setupErr != nil {
		t.Fatalf("fixture: %v", setupErr)
	}
	return cfg, pw
}

func setup(dsn string) error {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + itDB + " WITH (FORCE)",
		"CREATE DATABASE " + itDB,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			return err
		}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	cfg.Database = itDB
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, fixture, pgx.QueryExecModeSimpleProtocol)
	return err
}

func open(t testing.TB) driver.Conn {
	t.Helper()
	cfg, pw := itConfig(t)
	c, err := New().Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func newSession(t *testing.T, c driver.Conn, scope driver.Scope) driver.Session {
	t.Helper()
	s, err := c.NewSession(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// all executes text and reads the whole result.
func all(t *testing.T, s driver.Session, text string) (driver.Result, [][]any) {
	t.Helper()
	ctx := context.Background()
	r, err := s.Execute(ctx, driver.Statement{Text: text})
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	defer r.Close()
	var rows [][]any
	for {
		page, err := r.Next(ctx, 100)
		if err != nil {
			t.Fatalf("%s: next: %v", text, err)
		}
		if len(page) == 0 {
			return r, rows
		}
		rows = append(rows, page...)
	}
}

func TestITOpenWrongPassword(t *testing.T) {
	cfg, _ := itConfig(t)
	_, err := New().Open(context.Background(), cfg, "definitely-wrong")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "password authentication failed") || strings.Contains(err.Error(), "SQLSTATE") {
		t.Errorf("unfriendly error: %q", err)
	}

	cfg.Port = 1
	cfg.Options["connect_timeout"] = "2"
	_, err = New().Open(context.Background(), cfg, "x")
	if err == nil || !strings.Contains(err.Error(), "cannot connect") && !strings.Contains(err.Error(), "timed out") {
		t.Errorf("unreachable server: %v", err)
	}
	t.Logf("unreachable: %v", err)
}

type nodeSummary struct {
	Name, Detail string
	Kind         driver.NodeKind
	Leaf         bool
}

func children(t *testing.T, c driver.Conn, path ...string) []nodeSummary {
	t.Helper()
	var parent *driver.Node
	if len(path) > 0 {
		parent = &driver.Node{Path: path}
	}
	nodes, err := c.Children(context.Background(), parent)
	if err != nil {
		t.Fatalf("Children(%q): %v", path, err)
	}
	var out []nodeSummary
	for _, n := range nodes {
		if !slices.Equal(n.Path[:len(n.Path)-1], path) {
			t.Errorf("node %q has path %q, parent %q", n.Name, n.Path, path)
		}
		out = append(out, nodeSummary{n.Name, n.Detail, n.Kind, n.Leaf})
	}
	return out
}

func expect(t *testing.T, what string, got, want []nodeSummary) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %+v\nwant %+v", what, got, want)
	}
}

func TestITChildren(t *testing.T) {
	c := open(t)

	dbs := children(t, c)
	i := slices.IndexFunc(dbs, func(n nodeSummary) bool { return n.Name == itDB })
	if i < 0 || dbs[i].Kind != driver.NodeDatabase || !strings.HasSuffix(dbs[i].Detail, "B") {
		t.Fatalf("databases: %+v", dbs)
	}
	if slices.ContainsFunc(dbs, func(n nodeSummary) bool { return strings.HasPrefix(n.Name, "template") }) {
		t.Errorf("templates listed: %+v", dbs)
	}

	schemas := children(t, c, itDB)
	expect(t, "schemas", schemas, []nodeSummary{
		{"public", "", driver.NodeSchema, false},
		{"shop", "", driver.NodeSchema, false},
		{"information_schema", "system", driver.NodeSchema, false},
		{"pg_catalog", "system", driver.NodeSchema, false},
	})

	expect(t, "folders", children(t, c, itDB, "shop"), []nodeSummary{
		{"Tables", "(2)", driver.NodeFolder, false},
		{"Views", "(1)", driver.NodeFolder, false},
		{"Materialized views", "(1)", driver.NodeFolder, false},
		{"Functions", "(1)", driver.NodeFolder, false},
		{"Sequences", "(3)", driver.NodeFolder, false},
	})
	expect(t, "empty schema folders", children(t, c, itDB, "public"), []nodeSummary{
		{"Tables", "(0)", driver.NodeFolder, false},
	})

	expect(t, "tables", children(t, c, itDB, "shop", "tables"), []nodeSummary{
		{"customers", "~2", driver.NodeTable, false},
		{"orders", "~0", driver.NodeTable, false},
	})
	expect(t, "views", children(t, c, itDB, "shop", "views"), []nodeSummary{
		{"big_orders", "", driver.NodeView, false},
	})
	expect(t, "matviews", children(t, c, itDB, "shop", "matviews"), []nodeSummary{
		{"order_totals", "materialized", driver.NodeView, false},
	})
	expect(t, "functions", children(t, c, itDB, "shop", "functions"), []nodeSummary{
		{"add", "(integer, integer) → integer", driver.NodeFunction, true},
	})
	expect(t, "sequences", children(t, c, itDB, "shop", "sequences"), []nodeSummary{
		{"customers_id_seq", "", driver.NodeSequence, true},
		{"invoice_seq", "", driver.NodeSequence, true},
		{"orders_id_seq", "", driver.NodeSequence, true},
	})
	fn, err := c.Children(context.Background(), &driver.Node{Path: []string{itDB, "shop", "functions"}})
	if err != nil || len(fn) != 1 || fn[0].Path[3] != "add(integer, integer)" {
		t.Errorf("function path: %+v, %v", fn, err)
	}

	expect(t, "table sub-folders", children(t, c, itDB, "shop", "tables", "orders"), []nodeSummary{
		{"Columns", "(4)", driver.NodeFolder, false},
		{"Indexes", "(2)", driver.NodeFolder, false},
		{"Constraints", "(3)", driver.NodeFolder, false},
	})
	expect(t, "view sub-folders", children(t, c, itDB, "shop", "views", "big_orders"), []nodeSummary{
		{"Columns", "(4)", driver.NodeFolder, false},
	})
	expect(t, "columns", children(t, c, itDB, "shop", "tables", "orders", "columns"), []nodeSummary{
		{"id", "bigint not null identity", driver.NodeColumn, true},
		{"customer_id", "integer not null", driver.NodeColumn, true},
		{"total", "numeric(12,2)", driver.NodeColumn, true},
		{"status", "text = 'new'", driver.NodeColumn, true},
	})
	expect(t, "serial column", children(t, c, itDB, "shop", "tables", "customers", "columns")[:1], []nodeSummary{
		{"id", "integer not null = nextval('shop.customers_id_seq')", driver.NodeColumn, true},
	})
	expect(t, "indexes", children(t, c, itDB, "shop", "tables", "orders", "indexes"), []nodeSummary{
		{"orders_pkey", "btree (id) primary", driver.NodeIndex, true},
		{"orders_customer_idx", "btree (customer_id)", driver.NodeIndex, true},
	})
	expect(t, "unique index", children(t, c, itDB, "shop", "tables", "customers", "indexes")[1:], []nodeSummary{
		{"customers_email_key", "btree (email) unique", driver.NodeIndex, true},
	})
	expect(t, "constraints", children(t, c, itDB, "shop", "tables", "orders", "constraints"), []nodeSummary{
		{"orders_pkey", "PRIMARY KEY (id)", driver.NodeIndex, true},
		{"orders_customer_id_fkey", "FOREIGN KEY (customer_id) REFERENCES shop.customers(id)", driver.NodeIndex, true},
		{"orders_total_check", "CHECK ((total >= (0)::numeric))", driver.NodeIndex, true},
	})

	// Metadata is safe for concurrent use.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Children(context.Background(), &driver.Node{Path: []string{itDB, "shop", "tables"}}); err != nil {
				t.Error(err)
			}
			if _, err := c.Children(context.Background(), nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	c.Close()
	if _, err := c.Children(context.Background(), nil); !errors.Is(err, errClosed) {
		t.Errorf("after Close: %v", err)
	}
}

func TestITSessionScope(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB, Schema: "shop"})
	_, rows := all(t, s, "select current_database(), current_schema(), count(*) from customers")
	if want := []any{itDB, "shop", int64(2)}; !reflect.DeepEqual(rows[0], want) {
		t.Errorf("got %#v, want %#v", rows[0], want)
	}

	def := newSession(t, c, driver.Scope{})
	_, rows = all(t, def, "select current_database(), current_schema()")
	cfg, _ := itConfig(t)
	if rows[0][0] != cmp.Or(cfg.Database, "postgres") || rows[0][1] != "public" {
		t.Errorf("default scope: %#v", rows[0])
	}
}

func TestITTypes(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB, Schema: "shop"})
	q := `select 1::int2 a, 2::int4, 3::int8, 1.5::float4, 2.5::float8, 12.3450::numeric, 'NaN'::numeric,
	true, 'txt'::text, 'v'::varchar, 'c'::char(3), '{"b": [1, 2], "a": "x"}'::jsonb, '{"z" : 1}'::json,
	'\x00ff'::bytea, '2024-01-02 03:04:05+00'::timestamptz, '2024-01-02'::date, '2024-01-02 03:04:05'::timestamp,
	'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid, '1 day 2 hours'::interval, null::int,
	array[1, 2, null]::int[], array['a', 'b c']::text[], array[[1.5, 2], [3, 4]]::numeric[],
	'10.0.0.1'::inet, 'happy'::mood, array['happy', 'sad']::mood[], 'infinity'::timestamptz, 42::money,
	'12:30'::time, '(1,2)'::point`
	r, rows := all(t, s, q)
	if r.Affected() != -1 {
		t.Errorf("Affected = %d", r.Affected())
	}
	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	want := []any{
		int64(1), int64(2), int64(3), 1.5, 2.5, driver.Number("12.3450"), "NaN",
		true, "txt", "v", "c  ", driver.JSON(`{"a":"x","b":[1,2]}`), driver.JSON(`{"z":1}`),
		[]byte{0, 255}, ts, time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), ts,
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "1 day 02:00:00", nil,
		[]any{int64(1), int64(2), nil}, []any{"a", "b c"},
		[]any{[]any{driver.Number("1.5"), driver.Number("2")}, []any{driver.Number("3"), driver.Number("4")}},
		"10.0.0.1", "happy", []any{"happy", "sad"}, "infinity", "$42.00",
		"12:30:00", "(1,2)",
	}
	got := rows[0]
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d", len(got), len(want))
	}
	for i := range want {
		g := got[i]
		if tm, ok := g.(time.Time); ok {
			if w, ok := want[i].(time.Time); ok && tm.Equal(w) {
				continue
			}
		}
		if !reflect.DeepEqual(g, want[i]) {
			t.Errorf("col %d (%s): got %#v, want %#v", i, r.Columns()[i].Type, g, want[i])
		}
	}
	var types []string
	for _, col := range r.Columns() {
		types = append(types, col.Type)
	}
	wantTypes := []string{"int2", "int4", "int8", "float4", "float8", "numeric", "numeric", "bool", "text", "varchar",
		"bpchar", "jsonb", "json", "bytea", "timestamptz", "date", "timestamp", "uuid", "interval", "int4",
		"int4[]", "text[]", "numeric[]", "inet", "shop.mood", "shop.mood[]", "timestamptz", "money", "time", "point"}
	if !slices.Equal(types, wantTypes) {
		t.Errorf("types:\n got %q\nwant %q", types, wantTypes)
	}
	if r.Columns()[0].Name != "a" {
		t.Errorf("column name %q", r.Columns()[0].Name)
	}
}

func TestITDMLAndDDL(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB})
	ctx := context.Background()
	exec := func(text string) driver.Result {
		t.Helper()
		r, err := s.Execute(ctx, driver.Statement{Text: text})
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		return r
	}
	r := exec("create table dml (id int primary key, v text)")
	if r.Affected() != -1 || len(r.Columns()) != 0 {
		t.Errorf("DDL: affected %d, cols %v", r.Affected(), r.Columns())
	}
	if rows, err := r.Next(ctx, 10); err != nil || len(rows) != 0 {
		t.Errorf("DDL Next: %v, %v", rows, err)
	}
	if tag := r.(interface{ Tag() string }).Tag(); tag != "CREATE TABLE" {
		t.Errorf("tag %q", tag)
	}
	r.Close()
	r.Close()
	if r := exec("insert into dml select g, 'x' from generate_series(1, 3) g"); r.Affected() != 3 {
		t.Errorf("insert affected %d", r.Affected())
	}
	if r := exec("update dml set v = 'y' where id > 1"); r.Affected() != 2 {
		t.Errorf("update affected %d", r.Affected())
	}
	_, rows := all(t, s, "delete from dml where id = 1 returning id, v")
	if !reflect.DeepEqual(rows, [][]any{{int64(1), "x"}}) {
		t.Errorf("returning: %#v", rows)
	}
	if r := exec("drop table dml"); r.Affected() != -1 {
		t.Errorf("drop affected %d", r.Affected())
	}
	if _, err := s.Execute(ctx, driver.Statement{Text: "copy pg_class to stdout"}); !errors.Is(err, errCopy) {
		t.Errorf("COPY: %v", err)
	}
	if r, err := s.Execute(ctx, driver.Statement{Text: "-- nothing"}); err != nil || r.Affected() != -1 {
		t.Errorf("comment-only: %v", err)
	}
}

func TestITErrorPosition(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB})
	for _, text := range []string{
		"select 1 from nosuchtable",
		"select 'ção', '€', nosuchtable.x from (select 1) nosuchtable2",
	} {
		_, err := s.Execute(context.Background(), driver.Statement{Text: text})
		qe, ok := errors.AsType[*driver.QueryError](err)
		if !ok {
			t.Fatalf("%s: got %T %v", text, err, err)
		}
		want := strings.Index(text, "nosuchtable")
		if qe.Position != want || qe.Code == "" {
			t.Errorf("%s: position %d (code %s), want %d: %v", text, qe.Position, qe.Code, want, qe)
		}
	}
	_, err := s.Execute(context.Background(), driver.Statement{Text: "select 1/0"})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Code != "22012" || qe.Position != -1 {
		t.Errorf("division by zero: %#v", err)
	}
	all(t, s, "select 1") // session still usable
}

func TestITNotices(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB})
	all(t, s, "DO $$ BEGIN RAISE NOTICE 'hello %', 42; RAISE WARNING 'careful'; END $$")
	ms := s.(driver.MessageSource)
	got := ms.DrainMessages()
	want := []driver.Message{{Severity: "NOTICE", Text: "hello 42"}, {Severity: "WARNING", Text: "careful"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := ms.DrainMessages(); len(got) != 0 {
		t.Errorf("second drain: %+v", got)
	}
}

func TestITCancel(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Execute(ctx, driver.Statement{Text: "SELECT pg_sleep(30)"})
	took := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if took > 3*time.Second {
		t.Errorf("cancel took %v", took)
	}
	t.Logf("cancelled after %v: %v", took, err)
	_, rows := all(t, s, "select 7")
	if rows[0][0] != int64(7) {
		t.Errorf("after cancel: %#v", rows)
	}

	// Cancelling the Execute context after it returned must not affect paging.
	ctx2, cancel2 := context.WithCancel(context.Background())
	r, err := s.Execute(ctx2, driver.Statement{Text: "select g from generate_series(1, 5000) g"})
	cancel2()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		page, err := r.Next(context.Background(), 1000)
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
}

func TestITTransactions(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB, Schema: "shop"})
	tx := s.(driver.Transactional)
	ctx := context.Background()
	count := func() int64 {
		_, rows := all(t, s, "select count(*) from customers")
		return rows[0][0].(int64)
	}
	before := count()
	if tx.InTx() {
		t.Fatal("autocommit session in tx")
	}
	if err := tx.SetAutoCommit(ctx, false); err != nil {
		t.Fatal(err)
	}
	all(t, s, "insert into customers (name) values ('tmp')")
	if !tx.InTx() {
		t.Fatal("manual mode should open a transaction")
	}
	if got := count(); got != before+1 {
		t.Errorf("inside tx: %d", got)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if tx.InTx() {
		t.Error("still in tx after rollback")
	}
	if got := count(); got != before {
		t.Errorf("after rollback: %d, want %d", got, before)
	}
	tx.Rollback(ctx) // the count above opened a tx

	all(t, s, "insert into customers (name) values ('kept')")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	other := newSession(t, c, driver.Scope{Database: itDB, Schema: "shop"})
	if _, rows := all(t, other, "select count(*) from customers where name = 'kept'"); rows[0][0] != int64(1) {
		t.Errorf("commit not visible: %#v", rows)
	}

	// A failed statement leaves the tx aborted; Commit reports the rollback.
	all(t, s, "select 1")
	if _, err := s.Execute(ctx, driver.Statement{Text: "select 1/0"}); err == nil {
		t.Fatal("expected error")
	}
	if !tx.InTx() {
		t.Error("aborted tx should count as InTx")
	}
	if err := tx.Commit(ctx); err == nil {
		t.Error("commit of aborted tx should fail")
	}
	if tx.InTx() {
		t.Error("in tx after failed commit")
	}

	// Reading part of a result inside a transaction and closing it keeps the tx.
	r, err := s.Execute(ctx, driver.Statement{Text: "select g from generate_series(1, 200000) g"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(ctx, 10); err != nil {
		t.Fatal(err)
	}
	r.Close()
	all(t, s, "select 1")
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit after partial read: %v", err)
	}

	if err := tx.SetAutoCommit(ctx, true); err != nil {
		t.Fatal(err)
	}
	all(t, s, "select 1")
	if tx.InTx() {
		t.Error("autocommit select left a tx open")
	}
}

func TestITPaging(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB})
	ctx := context.Background()
	r, err := s.Execute(ctx, driver.Statement{Text: "select g, g::text from generate_series(1, 100000) g"})
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
			if row[0] != next || row[1] != fmt.Sprint(next) {
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

	// Abandoning a huge result outside a transaction returns promptly.
	r, err = s.Execute(ctx, driver.Statement{Text: "select generate_series(1, 100000000)"}) // streams, unlike FROM generate_series
	if err != nil {
		t.Fatal(err)
	}
	r.Next(ctx, 10)
	start := time.Now()
	r.Close()
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("early close took %v", took)
	}
	_, rows := all(t, s, "select 1")
	if rows[0][0] != int64(1) {
		t.Errorf("after early close: %#v", rows)
	}

	// Executing again closes a result that was left open.
	if _, err := s.Execute(ctx, driver.Statement{Text: "select g from generate_series(1, 10) g"}); err != nil {
		t.Fatal(err)
	}
	all(t, s, "select 2")
}
