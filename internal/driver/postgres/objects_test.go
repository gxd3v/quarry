package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestBuiltinFunctions(t *testing.T) {
	fns := BuiltinFunctions()
	if len(fns) < 100 {
		t.Errorf("only %d built-ins", len(fns))
	}
	ident := regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	for i, f := range fns {
		if !ident.MatchString(f) {
			t.Errorf("not a lower-case identifier: %q", f)
		}
		if i > 0 && fns[i-1] >= f {
			t.Errorf("not sorted/unique: %q, %q", fns[i-1], f)
		}
	}
	for _, f := range []string{"now", "coalesce", "count", "string_agg", "jsonb_set", "date_trunc", "generate_series"} {
		if !slices.Contains(fns, f) {
			t.Errorf("missing %q", f)
		}
	}
	fns[0] = "mutated"
	if BuiltinFunctions()[0] == "mutated" {
		t.Error("BuiltinFunctions returns shared storage")
	}
}

// TestITBuiltinFunctionsExist guards the curated list against typos: every
// name must be a pg_catalog function or one of the SQL special forms.
func TestITBuiltinFunctionsExist(t *testing.T) {
	cfg, pw := itConfig(t)
	c, err := New().Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var missing []string
	err = c.(*conn).list(context.Background(), "", `
SELECT f FROM unnest($1::text[]) f
WHERE NOT EXISTS (SELECT FROM pg_proc p WHERE p.proname = f AND p.pronamespace = 'pg_catalog'::regnamespace)
ORDER BY 1`, []any{BuiltinFunctions()}, func(r pgx.Rows) error {
		var f string
		err := r.Scan(&f)
		missing = append(missing, f)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	special := []string{"coalesce", "current_date", "current_time", "current_timestamp", "greatest",
		"least", "localtime", "localtimestamp", "nullif", "trim"}
	for _, f := range missing {
		if !slices.Contains(special, f) {
			t.Errorf("%q is neither a pg_catalog function nor SQL syntax", f)
		}
	}
}

type objSummary struct {
	Schema, Name string
	Kind         driver.NodeKind
	Detail       string
	Columns      string // "name type [null], ..."
}

func summarize(objs []driver.Object, schema string) []objSummary {
	var out []objSummary
	for _, o := range objs {
		if schema != "" && o.Schema != schema {
			continue
		}
		var cols []string
		for _, c := range o.Columns {
			s := c.Name + " " + c.Type
			if c.Nullable {
				s += " null"
			}
			cols = append(cols, s)
		}
		out = append(out, objSummary{o.Schema, o.Name, o.Kind, o.Detail, strings.Join(cols, ", ")})
	}
	return out
}

func TestITObjects(t *testing.T) {
	c := open(t)
	cat, ok := c.(driver.Catalog)
	if !ok {
		t.Fatal("conn does not implement driver.Catalog")
	}
	ctx := context.Background()
	objs, err := cat.Objects(ctx, driver.Scope{Database: itDB})
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(objs, "shop")
	want := []objSummary{
		{"shop", "add", driver.NodeFunction, "(integer, integer) → integer", ""},
		{"shop", "big_orders", driver.NodeView, "", "id bigint null, customer_id integer null, total numeric(12,2) null, status text null"},
		{"shop", "customers", driver.NodeTable, "", "id integer, name text, email character varying(100) null"},
		{"shop", "customers_id_seq", driver.NodeSequence, "", ""},
		{"shop", "invoice_seq", driver.NodeSequence, "", ""},
		{"shop", "order_totals", driver.NodeView, "materialized", "customer_id integer null, total numeric null"},
		{"shop", "orders", driver.NodeTable, "", "id bigint, customer_id integer, total numeric(12,2) null, status text null"},
		{"shop", "orders_id_seq", driver.NodeSequence, "", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shop objects:\n got %+v\nwant %+v", got, want)
	}
	for _, o := range objs {
		if o.Schema == "pg_catalog" || o.Schema == "information_schema" || strings.HasPrefix(o.Schema, "pg_") {
			t.Errorf("system object listed: %s.%s", o.Schema, o.Name)
		}
	}
	if !slices.IsSortedFunc(objs, func(a, b driver.Object) int {
		return strings.Compare(a.Schema+"\x00"+a.Name, b.Schema+"\x00"+b.Name)
	}) {
		t.Error("objects not sorted by schema, name")
	}

	// Default database, and concurrent use alongside the explorer.
	if _, err := cat.Objects(ctx, driver.Scope{}); err != nil {
		t.Errorf("default database: %v", err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if o, err := cat.Objects(ctx, driver.Scope{Database: itDB}); err != nil || !reflect.DeepEqual(summarize(o, "shop"), want) {
				t.Errorf("concurrent Objects: %v", err)
			}
			if _, err := c.Children(ctx, &driver.Node{Path: []string{itDB, "shop", "tables"}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if _, err := cat.Objects(ctx, driver.Scope{Database: "quarry_no_such_db"}); err == nil {
		t.Error("missing database: expected an error")
	}
	c.Close()
	if _, err := cat.Objects(ctx, driver.Scope{Database: itDB}); !errors.Is(err, errClosed) {
		t.Errorf("after Close: %v", err)
	}
}

func TestITSearchPath(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	searchPath := func(s driver.Session) []string {
		t.Helper()
		sp, ok := s.(interface {
			SearchPath(context.Context) ([]string, error)
		})
		if !ok {
			t.Fatal("session does not implement SearchPath")
		}
		path, err := sp.SearchPath(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}

	s := newSession(t, c, driver.Scope{Database: itDB, Schema: "shop"})
	if got := searchPath(s); !slices.Equal(got, []string{"shop", "public"}) {
		t.Errorf("scoped session: %q", got)
	}
	if got := searchPath(newSession(t, c, driver.Scope{Database: itDB})); !slices.Equal(got, []string{"public"}) {
		t.Errorf("default scope: %q", got)
	}
	all(t, s, `set search_path = shop, "no such schema", pg_catalog`)
	if got := searchPath(s); !slices.Equal(got, []string{"shop", "pg_catalog"}) {
		t.Errorf("after SET: %q", got)
	}

	// A result still being paged is not discarded.
	r, err := s.Execute(ctx, driver.Statement{Text: "select g from generate_series(1, 100000) g"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.(SearchPathSource).SearchPath(ctx); !errors.Is(err, errBusy) {
		t.Errorf("with open result: %v", err)
	}
	if page, err := r.Next(ctx, 10); err != nil || len(page) != 10 || page[0][0] != int64(11) {
		t.Errorf("result disturbed: %v %v", page, err)
	}
	r.Close()
	searchPath(s)

	// Inside a transaction a cancelled context must not abort it.
	tx := s.(driver.Transactional)
	if err := tx.SetAutoCommit(ctx, false); err != nil {
		t.Fatal(err)
	}
	all(t, s, "select 1")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.(SearchPathSource).SearchPath(cancelled); err != nil {
		t.Errorf("in tx with cancelled ctx: %v", err)
	}
	if !tx.InTx() {
		t.Error("transaction lost")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit after SearchPath: %v", err)
	}
}

// BenchmarkITObjects measures Objects on the small fixture and on a database
// with thousands of tables (created once, kept between runs).
func BenchmarkITObjects(b *testing.B) {
	c := open(b)
	cat := c.(driver.Catalog)
	ctx := context.Background()
	b.Run("fixture", func(b *testing.B) {
		for b.Loop() {
			if _, err := cat.Objects(ctx, driver.Scope{Database: itDB}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("bulk", func(b *testing.B) {
		const tables, cols, funcs = 5000, 12, 1000
		db := bulkDB(b, tables, cols, funcs)
		var objs []driver.Object
		for b.Loop() {
			var err error
			if objs, err = cat.Objects(ctx, driver.Scope{Database: db}); err != nil {
				b.Fatal(err)
			}
		}
		// Sorted by name: f00001..f01000, then t00001..t05000.
		if len(objs) != tables+funcs || objs[funcs].Kind != driver.NodeTable || len(objs[funcs].Columns) != cols {
			b.Fatalf("got %d objects, first table %+v", len(objs), objs[min(funcs, len(objs)-1)])
		}
	})
}

func bulkDB(b *testing.B, tables, cols, funcs int) string {
	b.Helper()
	db := fmt.Sprintf("quarry_it_bulk_%d_%d_%d", tables, cols, funcs)
	itConfig(b) // skips without a DSN
	pcfg, err := pgx.ParseConfig(os.Getenv("QUARRY_TEST_PG_DSN"))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, pcfg)
	if err != nil {
		b.Fatal(err)
	}
	defer admin.Close(ctx)
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", db).Scan(&exists); err != nil {
		b.Fatal(err)
	}
	if exists {
		return db
	}
	// Built under a temporary name and renamed once complete, so an
	// interrupted run never leaves a half-filled database behind.
	tmp := db + "_tmp"
	for _, q := range []string{"DROP DATABASE IF EXISTS " + tmp + " WITH (FORCE)", "CREATE DATABASE " + tmp} {
		if _, err := admin.Exec(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
	pcfg.Database = tmp
	bc, err := pgx.ConnectConfig(ctx, pcfg)
	if err != nil {
		b.Fatal(err)
	}
	colDefs := make([]string, cols)
	for i := range colDefs {
		colDefs[i] = fmt.Sprintf("c%d %s", i+1, []string{"int", "text NOT NULL", "numeric(10,2)"}[i%3])
	}
	stmts := []string{"CREATE SCHEMA bulk"}
	// One transaction per chunk keeps the lock table from overflowing.
	const chunk = 500
	for lo := 1; lo <= tables; lo += chunk {
		stmts = append(stmts, fmt.Sprintf(`DO $$ BEGIN FOR t IN %d..%d LOOP
	EXECUTE format('CREATE TABLE bulk.t%%s (%s)', lpad(t::text, 5, '0'));
END LOOP; END $$`, lo, min(lo+chunk-1, tables), strings.Join(colDefs, ", ")))
	}
	stmts = append(stmts, fmt.Sprintf(`DO $$ BEGIN FOR f IN 1..%d LOOP
	EXECUTE format('CREATE FUNCTION bulk.f%%s(a int, b text) RETURNS int LANGUAGE sql AS ''select a''', lpad(f::text, 5, '0'));
END LOOP; END $$`, funcs))
	for _, q := range stmts {
		if _, err := bc.Exec(ctx, q); err != nil {
			bc.Close(ctx)
			b.Fatal(err)
		}
	}
	bc.Close(ctx)
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+tmp+" RENAME TO "+db); err != nil {
		b.Fatal(err)
	}
	return db
}
