package oracle

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

func node(folder, name string) *driver.Node {
	return &driver.Node{Path: []string{itUser, folder, name}}
}

func TestITDataQuery(t *testing.T) {
	c := openUser(t)
	dv := c.(driver.DataViewer)
	s := newSession(t, c, driver.Scope{})
	run := func(target *driver.Node, filter string, orderBy ...string) (driver.Result, [][]any) {
		t.Helper()
		q, err := dv.DataQuery(target, filter, orderBy)
		if err != nil {
			t.Fatal(err)
		}
		return all(t, s, q)
	}
	mixed := node(folderTables, "MixedCase")
	if _, rows := run(mixed, "", `"N" DESC`); !reflect.DeepEqual(rows, [][]any{
		{int64(4), nil, nil}, {int64(1), "one", int64(30)}, {int64(3), "three", int64(20)}, {int64(2), "two", int64(10)},
	}) {
		t.Errorf("order by N desc: %#v", rows)
	}
	if _, rows := run(mixed, `WHERE "Label" LIKE 't%' -- comment;`, "Id"); len(rows) != 2 || rows[0][0] != int64(2) {
		t.Errorf("filter: %#v", rows)
	}
	if _, rows := run(mixed, "", "N ASC", `"Label" DESC`); rows[0][0] != int64(2) {
		t.Errorf("multiple terms: %#v", rows)
	}
	if _, err := dv.DataQuery(mixed, "", []string{"Id; drop table x"}); err == nil {
		t.Error("injection in sort term accepted")
	}
	if _, err := dv.DataQuery(&driver.Node{Path: []string{itUser, "tables", `X" t; --`}}, "", nil); err == nil {
		t.Error("quote in table name accepted")
	}

	// A table without a key gets its ROWID.
	r, rows := run(node(folderTables, "NOKEY"), "", "A")
	cols := r.Columns()
	if len(cols) != 3 || cols[2].Name != "ROWID" || len(rows) != 3 {
		t.Errorf("nokey: %v %#v", cols, rows)
	}
	if r, _ := run(node(folderTables, "CUSTOMERS"), ""); len(r.Columns()) != 3 {
		t.Errorf("keyed table should not get a ROWID: %v", r.Columns())
	}
	if _, rows := run(node(folderViews, "BIG_ORDERS"), ""); len(rows) != 0 {
		t.Errorf("view: %#v", rows)
	}
	if _, err := dv.DataQuery(node(folderSequences, "INVOICE_SEQ"), "", nil); err == nil {
		t.Error("sequence accepted")
	}
}

func TestITRowKey(t *testing.T) {
	c := openUser(t)
	ed := c.(driver.Editable)
	for _, tt := range []struct {
		target *driver.Node
		want   []string
	}{
		{node(folderTables, "CUSTOMERS"), []string{"ID"}},
		{node(folderTables, "COMPOSITE"), []string{"K2", "K1"}},
		{node(folderTables, "NOPK_UQ"), []string{"B", "A"}},
		{node(folderTables, "NOKEY"), []string{"ROWID"}},
		{node(folderTables, "MixedCase"), []string{"Id"}},
		{node(folderViews, "BIG_ORDERS"), nil},
		{node(folderMatviews, "ORDER_TOTALS"), nil},
	} {
		got, err := ed.RowKey(context.Background(), tt.target)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("RowKey(%s) = %q, %v; want %q", tt.target.Path[2], got, err, tt.want)
		}
	}
	if _, err := ed.RowKey(context.Background(), node(folderTables, "NOSUCH")); err == nil {
		t.Error("missing table should fail")
	}
}

func TestITApply(t *testing.T) {
	c := openUser(t)
	ed := c.(driver.Editable)
	s := newSession(t, c, driver.Scope{})
	ctx := context.Background()
	items := node(folderTables, "ITEMS")
	ts := time.Date(2024, 5, 6, 7, 8, 9, 123456000, time.UTC)
	tz := time.Date(2024, 5, 6, 7, 8, 9, 0, time.FixedZone("", 2*3600))

	err := ed.Apply(ctx, items, []driver.RowChange{
		{Op: driver.OpInsert, Values: map[string]any{
			"ID": int64(999), "SKU": "C-3", "QTY": int64(4), "PRICE": driver.Number("2.5"), "TOTAL": int64(1),
			"NAME": "o'clock", "CREATED": "2024-05-06", "STAMP": ts, "STAMP_TZ": tz,
			"RAW_DATA": []byte{1, 2, 3}, "NOTES": strings.Repeat("long text ", 5000), "DATA": driver.JSON(`{"k":[1,2]}`),
			"FLAG": true,
		}},
		{Op: driver.OpUpdate, Key: map[string]any{"ID": int64(1)}, Values: map[string]any{"PRICE": "12.75", "CREATED": "2023-01-02 03:04:05", "RAW_DATA": "0aff"}},
		{Op: driver.OpDelete, Key: map[string]any{"ID": "2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, rows := all(t, s, "select sku, qty, price, total, name, created, stamp, stamp_tz, raw_data, length(notes), json_serialize(data), flag from items order by id")
	if len(rows) != 2 {
		t.Fatalf("rows: %#v", rows)
	}
	a, b := rows[0], rows[1]
	if a[2] != driver.Number("12.75") || !a[5].(time.Time).Equal(time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)) || !reflect.DeepEqual(a[8], []byte{0x0a, 0xff}) {
		t.Errorf("updated row: %#v", a)
	}
	if b[0] != "C-3" || b[2] != driver.Number("2.5") || b[3] != int64(10) || b[4] != "o'clock" ||
		!b[5].(time.Time).Equal(time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)) || !b[6].(time.Time).Equal(ts) ||
		!b[7].(time.Time).Equal(tz) || !reflect.DeepEqual(b[8], []byte{1, 2, 3}) || b[9] != int64(50000) ||
		b[10] != `{"k":[1,2]}` || b[11] != int64(1) {
		t.Errorf("inserted row: %#v", b)
	}

	// Conflicts roll everything back.
	err = ed.Apply(ctx, items, []driver.RowChange{
		{Op: driver.OpUpdate, Key: map[string]any{"ID": int64(1)}, Values: map[string]any{"NAME": "changed"}},
		{Op: driver.OpDelete, Key: map[string]any{"ID": int64(424242)}},
	})
	if err == nil || !strings.Contains(err.Error(), "changed or was deleted") {
		t.Errorf("stale delete: %v", err)
	}
	if _, rows := all(t, s, "select name from items where id = 1"); rows[0][0] != "apple" {
		t.Errorf("not rolled back: %#v", rows)
	}
	err = ed.Apply(ctx, items, []driver.RowChange{{Op: driver.OpInsert, Values: map[string]any{"SKU": "A-1"}}})
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Code != "ORA-00001" || !strings.HasPrefix(qe.Msg, "inserting new row 1: ") {
		t.Errorf("unique violation: %#v", err)
	}
	err = ed.Apply(ctx, items, []driver.RowChange{{Op: driver.OpUpdate, Key: map[string]any{"ID": int64(1)}, Values: map[string]any{"NOPE": 1}}})
	if err == nil || !strings.Contains(err.Error(), `unknown column "NOPE"`) {
		t.Errorf("unknown column: %v", err)
	}
	err = ed.Apply(ctx, items, []driver.RowChange{{Op: driver.OpUpdate, Key: map[string]any{"ID": int64(1)}, Values: map[string]any{"CREATED": "not a date"}}})
	if _, ok := errors.AsType[*driver.QueryError](err); !ok {
		t.Errorf("bad date: %#v", err)
	}

	// ROWID keys; duplicates match more than one row only by value.
	_, rows = all(t, s, "select rowid, a from nokey where a = 2")
	err = ed.Apply(ctx, node(folderTables, "NOKEY"), []driver.RowChange{
		{Op: driver.OpUpdate, Key: map[string]any{"ROWID": rows[0][0]}, Values: map[string]any{"B": "z", "ROWID": "ignored"}},
		{Op: driver.OpDelete, Key: map[string]any{"ROWID": rows[1][0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, rows := all(t, s, "select a, b from nokey order by a"); !reflect.DeepEqual(rows, [][]any{{int64(1), "x"}, {int64(2), "z"}}) {
		t.Errorf("nokey after ROWID edits: %#v", rows)
	}

	// Mixed-case names, NULL keys, composite keys, DEFAULT-only inserts.
	err = ed.Apply(ctx, node(folderTables, "MixedCase"), []driver.RowChange{
		{Op: driver.OpUpdate, Key: map[string]any{"Id": int64(4)}, Values: map[string]any{"Label": "four"}},
		{Op: driver.OpInsert, Values: map[string]any{"Id": int64(5)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = ed.Apply(ctx, node(folderTables, "COMPOSITE"), []driver.RowChange{
		{Op: driver.OpUpdate, Key: map[string]any{"K1": "a", "K2": int64(1)}, Values: map[string]any{"V": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = ed.Apply(ctx, node(folderTables, "NOPK_UQ"), []driver.RowChange{{Op: driver.OpDelete, Key: map[string]any{"A": nil, "B": "x"}}})
	if err == nil || !strings.Contains(err.Error(), "A=NULL") {
		t.Errorf("NULL key: %v", err)
	}
	if err := ed.Apply(ctx, node(folderViews, "BIG_ORDERS"), []driver.RowChange{{Op: driver.OpInsert}}); err == nil {
		t.Error("editing a view should fail")
	}
}

func TestITDescribe(t *testing.T) {
	c := openUser(t)
	desc := c.(driver.Describer)
	d, err := desc.Describe(context.Background(), node(folderTables, "ITEMS"))
	if err != nil {
		t.Fatal(err)
	}
	col := func(name string) driver.ColumnInfo {
		i := slices.IndexFunc(d.Columns, func(c driver.ColumnInfo) bool { return c.Name == name })
		if i < 0 {
			t.Fatalf("column %s missing", name)
		}
		return d.Columns[i]
	}
	if c := col("ID"); !c.PrimaryKey || c.Nullable || c.Default != "GENERATED ALWAYS AS IDENTITY" || c.Type != "NUMBER" {
		t.Errorf("ID: %+v", c)
	}
	if c := col("SKU"); c.Comment != "Stock keeping unit, it's unique" || c.Type != "VARCHAR2(20)" {
		t.Errorf("SKU: %+v", c)
	}
	if c := col("QTY"); c.Default != "1" || c.Nullable {
		t.Errorf("QTY: %+v", c)
	}
	if c := col("TOTAL"); c.Default != `GENERATED ALWAYS AS ("QTY"*"PRICE") VIRTUAL` {
		t.Errorf("TOTAL: %+v", c)
	}
	if c := col("STAMP_TZ"); c.Type != "TIMESTAMP(6) WITH TIME ZONE" {
		t.Errorf("STAMP_TZ: %+v", c)
	}
	if len(d.Indexes) != 3 || !slices.Contains(d.Indexes, driver.DetailRow{Name: "ITEMS_NAME_IDX",
		Definition: `CREATE INDEX QUARRY_IT.ITEMS_NAME_IDX ON QUARRY_IT.ITEMS (UPPER("NAME"), "QTY" DESC)`}) {
		t.Errorf("indexes: %+v", d.Indexes)
	}
	if len(d.Constraints) != 3 || d.Constraints[0].Definition != "PRIMARY KEY (ID)" {
		t.Errorf("constraints: %+v", d.Constraints)
	}
	stats := map[string]string{}
	for _, s := range d.Stats {
		stats[s.Name] = s.Definition
	}
	if stats["Estimated rows"] == "" || stats["Table size"] == "n/a" || stats["Last analyzed"] == "never" {
		t.Errorf("stats: %+v", d.Stats)
	}
	t.Logf("stats: %+v", d.Stats)
	t.Logf("DDL:\n%s", d.DDL)
	for _, want := range []string{`CREATE TABLE "QUARRY_IT"."ITEMS"`, `CREATE INDEX "QUARRY_IT"."ITEMS_NAME_IDX"`,
		`COMMENT ON TABLE "QUARRY_IT"."ITEMS"  IS 'Stock items';`, "GENERATED ALWAYS AS IDENTITY"} {
		if !strings.Contains(d.DDL, want) {
			t.Errorf("DDL lacks %q", want)
		}
	}
	for _, unwanted := range []string{"STORAGE(", "TABLESPACE", "PCTFREE", `INDEX "QUARRY_IT"."SYS_C`} {
		if strings.Contains(d.DDL, unwanted) {
			t.Errorf("DDL has %q", unwanted)
		}
	}

	d, err = desc.Describe(context.Background(), node(folderTables, "ORDERS"))
	if err != nil || len(d.Triggers) != 1 || d.Triggers[0].Definition != "BEFORE EACH ROW INSERT" || !strings.Contains(d.DDL, "CREATE OR REPLACE EDITIONABLE TRIGGER") {
		t.Errorf("orders: %+v, %v", d.Triggers, err)
	}
	d, err = desc.Describe(context.Background(), node(folderViews, "BIG_ORDERS"))
	if err != nil || len(d.Columns) != 4 || d.Stats != nil || !strings.Contains(d.DDL, `VIEW "QUARRY_IT"."BIG_ORDERS"`) {
		t.Errorf("view: %+v, %v", d, err)
	}
	d, err = desc.Describe(context.Background(), node(folderMatviews, "ORDER_TOTALS"))
	if err != nil || !strings.Contains(d.DDL, "CREATE MATERIALIZED VIEW") || !slices.ContainsFunc(d.Stats, func(r driver.DetailRow) bool { return r.Name == "Last refresh" }) {
		t.Errorf("matview: %+v, %v", d, err)
	}
	d, err = desc.Describe(context.Background(), node(folderPackages, "PK"))
	if err != nil || !strings.Contains(d.DDL, "PACKAGE BODY") || d.Columns != nil {
		t.Errorf("package: %+v, %v", d, err)
	}
	for _, n := range []*driver.Node{node(folderSequences, "INVOICE_SEQ"), node(folderFunctions, "ADD_NUM"), node(folderSynonyms, "CUST")} {
		if d, err := desc.Describe(context.Background(), n); err != nil || !strings.HasPrefix(d.DDL, "CREATE") {
			t.Errorf("%s: %q, %v", n.Path[2], d.DDL, err)
		}
	}
	if _, err := desc.Describe(context.Background(), node(folderTables, "NOSUCH")); err == nil {
		t.Error("missing table should fail")
	}

	// Without SELECT_CATALOG_ROLE other schemas' DDL is unavailable.
	sys, err := desc.Describe(context.Background(), &driver.Node{Path: []string{"SYS", folderViews, "DBA_USERS"}})
	if err == nil && !strings.HasPrefix(sys.DDL, "-- DDL unavailable") {
		t.Errorf("SYS view as QUARRY_IT: %q", sys.DDL)
	}
}

func TestITObjects(t *testing.T) {
	c := openUser(t)
	objs, err := c.(driver.Catalog).Objects(context.Background(), driver.Scope{Schema: itUser})
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) driver.Object {
		i := slices.IndexFunc(objs, func(o driver.Object) bool { return o.Schema == itUser && o.Name == name })
		if i < 0 {
			t.Fatalf("%s missing", name)
		}
		return objs[i]
	}
	if o := find("ORDERS"); o.Kind != driver.NodeTable || len(o.Columns) != 4 || o.Columns[2] != (driver.Column{Name: "TOTAL", Type: "NUMBER(12,2)", Nullable: true}) {
		t.Errorf("orders: %+v", o)
	}
	if o := find("BIG_ORDERS"); o.Kind != driver.NodeView {
		t.Errorf("view: %+v", o)
	}
	if o := find("ORDER_TOTALS"); o.Kind != driver.NodeView || o.Detail != "materialized" {
		t.Errorf("matview: %+v", o)
	}
	if o := find("ADD_NUM"); o.Kind != driver.NodeFunction || o.Detail != "(A NUMBER, B NUMBER) → NUMBER" {
		t.Errorf("function: %+v", o)
	}
	if o := find("PK"); o.Detail != "package" {
		t.Errorf("package: %+v", o)
	}
	if o := find("INVOICE_SEQ"); o.Kind != driver.NodeSequence {
		t.Errorf("sequence: %+v", o)
	}
	if slices.ContainsFunc(objs, func(o driver.Object) bool { return o.Schema == "SYS" || strings.HasPrefix(o.Name, "ISEQ$$") }) {
		t.Error("system objects listed")
	}
	if !slices.IsSortedFunc(objs, func(a, b driver.Object) int {
		return strings.Compare(a.Schema+"\x00"+a.Name, b.Schema+"\x00"+b.Name)
	}) {
		t.Error("not sorted")
	}
}
