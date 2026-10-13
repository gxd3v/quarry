package postgres

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestDataQuery(t *testing.T) {
	tests := []struct {
		filter  string
		orderBy []string
		want    string
	}{
		{"", nil, `SELECT * FROM "shop"."Orders"`},
		{"  ", []string{"id"}, `SELECT * FROM "shop"."Orders" ORDER BY "id"`},
		{"total > 10;; ", []string{"Total DESC", "id asc", `"my col" desc`, `"a""b"`},
			`SELECT * FROM "shop"."Orders" WHERE (total > 10) ORDER BY "Total" DESC, "id", "my col" DESC, "a""b"`},
		{"WHERE a = 1 OR b = 2", nil, `SELECT * FROM "shop"."Orders" WHERE (a = 1 OR b = 2)`},
		{"a = 1 -- note", []string{"a"}, "SELECT * FROM \"shop\".\"Orders\" WHERE (a = 1 -- note\n) ORDER BY \"a\""},
		{"", []string{"desc"}, `SELECT * FROM "shop"."Orders" ORDER BY "desc"`},
		{"", []string{"id;drop"}, `SELECT * FROM "shop"."Orders" ORDER BY "id;drop"`},
	}
	c := &conn{}
	for _, tt := range tests {
		got, err := c.DataQuery(&driver.Node{Path: []string{"db", "shop", folderTables, "Orders"}}, tt.filter, tt.orderBy)
		if err != nil || got != tt.want {
			t.Errorf("DataQuery(%q, %q):\n got %q, %v\nwant %q", tt.filter, tt.orderBy, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", " ", "id; DROP TABLE x", "id DESC NULLS LAST", "my col", `"unterminated`, `"a"b"`, `""`, "a\x00"} {
		if q, err := c.DataQuery(&driver.Node{Path: []string{"db", "s", folderViews, "v"}}, "", []string{bad}); err == nil {
			t.Errorf("orderBy %q accepted: %q", bad, q)
		}
	}
	for _, p := range [][]string{nil, {"db", "s", folderFunctions, "f()"}, {"db", "s", folderTables}, {"db", "s", folderTables, "t", "columns"}} {
		if _, err := c.DataQuery(&driver.Node{Path: p}, "", nil); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if _, err := c.DataQuery(nil, "", nil); err == nil {
		t.Error("nil node accepted")
	}
}

var testCols = []colMeta{
	{name: "id", typ: "integer", identity: "a"},
	{name: "Code", typ: "character varying"},
	{name: "qty", typ: "integer"},
	{name: "total", typ: "numeric", generated: "s"},
	{name: "seq", typ: "bigint", identity: "d"},
	{name: "tags", typ: "text[]"},
}

func argTexts(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		if tx := a.(pgtype.Text); tx.Valid {
			out[i] = tx.String
		}
	}
	return out
}

func TestBuildChange(t *testing.T) {
	const table = `"s"."t"`
	tests := []struct {
		name  string
		ch    driver.RowChange
		sql   string
		args  []any
		label string
	}{
		{
			name:  "update",
			ch:    driver.RowChange{Op: driver.OpUpdate, Key: map[string]any{"id": int64(42)}, Values: map[string]any{"qty": "5", "Code": nil}},
			sql:   `UPDATE "s"."t" SET "Code" = $1::text::character varying, "qty" = $2::text::integer WHERE "id" = $3::text::integer`,
			args:  []any{nil, "5", "42"},
			label: "updating row id=42",
		},
		{
			name:  "update with NULL and composite key",
			ch:    driver.RowChange{Op: driver.OpUpdate, Key: map[string]any{"qty": nil, "Code": "a'b"}, Values: map[string]any{"tags": []any{"x", "y z", nil}}},
			sql:   `UPDATE "s"."t" SET "tags" = $1::text::text[] WHERE "Code" = $2::text::character varying AND "qty" IS NULL`,
			args:  []any{`{"x","y z",NULL}`, "a'b"},
			label: "updating row Code='a''b', qty=NULL",
		},
		{
			name:  "delete",
			ch:    driver.RowChange{Op: driver.OpDelete, Key: map[string]any{"id": "7"}},
			sql:   `DELETE FROM "s"."t" WHERE "id" = $1::text::integer`,
			args:  []any{"7"},
			label: "deleting row id=7",
		},
		{
			name:  "insert omits generated and identity",
			ch:    driver.RowChange{Op: driver.OpInsert, Values: map[string]any{"id": "1", "total": "3", "seq": nil, "qty": nil, "Code": "c"}},
			sql:   `INSERT INTO "s"."t" ("Code", "qty") VALUES ($1::text::character varying, $2::text::integer)`,
			args:  []any{"c", nil},
			label: "inserting new row 3",
		},
		{
			name:  "insert by-default identity value",
			ch:    driver.RowChange{Op: driver.OpInsert, Values: map[string]any{"seq": int64(9)}},
			sql:   `INSERT INTO "s"."t" ("seq") VALUES ($1::text::bigint)`,
			args:  []any{"9"},
			label: "inserting new row 3",
		},
		{
			name:  "insert defaults",
			ch:    driver.RowChange{Op: driver.OpInsert, Values: map[string]any{"id": nil}},
			sql:   `INSERT INTO "s"."t" DEFAULT VALUES`,
			label: "inserting new row 3",
		},
		{
			name: "empty update",
			ch:   driver.RowChange{Op: driver.OpUpdate, Key: map[string]any{"id": "1"}},
		},
	}
	for _, tt := range tests {
		st, err := buildChange(table, testCols, tt.ch, 3)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if st.sql != tt.sql || st.label != tt.label || len(st.args) != len(tt.args) ||
			len(tt.args) > 0 && !reflect.DeepEqual(argTexts(st.args), tt.args) {
			t.Errorf("%s:\n got %q %q %q\nwant %q %q %q", tt.name, st.sql, argTexts(st.args), st.label, tt.sql, tt.args, tt.label)
		}
	}

	for name, ch := range map[string]driver.RowChange{
		"unknown value column": {Op: driver.OpUpdate, Key: map[string]any{"id": "1"}, Values: map[string]any{"nope": "1"}},
		"unknown key column":   {Op: driver.OpDelete, Key: map[string]any{"ID": "1"}},
		"missing key":          {Op: driver.OpDelete},
		"bad value type":       {Op: driver.OpInsert, Values: map[string]any{"qty": struct{}{}}},
		"bad op":               {Op: driver.ChangeOp(9), Key: map[string]any{"id": "1"}},
	} {
		if _, err := buildChange(table, testCols, ch, 1); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestTextValue(t *testing.T) {
	ts := time.Date(2024, 1, 2, 3, 4, 5, 600000000, time.FixedZone("x", -3*3600))
	for _, tt := range []struct {
		in   any
		want string
	}{
		{"x", "x"},
		{driver.Number("1.50"), "1.50"},
		{driver.JSON(`{"a":1}`), `{"a":1}`},
		{true, "true"},
		{int64(-3), "-3"},
		{1.5, "1.5"},
		{math.Inf(-1), "-Infinity"},
		{math.NaN(), "NaN"},
		{[]byte{0, 0xff}, `\x00ff`},
		{ts, "2024-01-02 03:04:05.6-03:00"},
		{time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), "2024-01-02 00:00:00Z"},
		{[]any{[]any{int64(1), nil}, []any{`a"b\c`, ""}}, `{{"1",NULL},{"a\"b\\c",""}}`},
		{[]any{}, "{}"},
	} {
		got, ok, err := textValue(tt.in)
		if err != nil || !ok || got != tt.want {
			t.Errorf("textValue(%#v) = %q, %v, %v; want %q", tt.in, got, ok, err, tt.want)
		}
	}
	if _, ok, err := textValue(nil); ok || err != nil {
		t.Errorf("nil: ok=%v err=%v", ok, err)
	}
	if _, _, err := textValue(map[string]int{}); err == nil {
		t.Error("map accepted")
	}
}

func TestGroupDigits(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -12345: "-12,345"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDDL(t *testing.T) {
	r := &relDef{
		kind: "r", qname: "s.items", persistence: "u", options: []string{"fillfactor=70", "x=a b"},
		comment: "It's stock", rls: true,
		cols: []colDef{
			{qname: "id", typ: "integer", identity: "a", notNull: true, local: true, pk: true},
			{qname: `"Name"`, typ: "text", collation: `pg_catalog."C"`, def: "'x'::text", notNull: true, local: true, comment: "the name"},
			{qname: "total", typ: "numeric", generated: "s", def: "(qty * 2)", local: true},
			{qname: "inh", typ: "int", local: false},
		},
		cons: []conDef{
			{qname: "items_pkey", def: "PRIMARY KEY (id)", local: true, comment: "pk"},
			{qname: "inh_check", def: "CHECK (inh > 0)", local: false},
		},
		idx: []idxDef{
			{qname: "s.items_pkey", def: "CREATE UNIQUE INDEX items_pkey ON s.items USING btree (id)", constraint: true},
			{qname: "s.items_name", def: "CREATE INDEX items_name ON s.items USING btree (\"Name\")", comment: "by name"},
			{qname: "s.items_part", def: "CREATE INDEX items_part ON s.items USING btree (id)", inherited: true},
		},
		trg: []trgDef{
			{qname: "t1", def: "CREATE TRIGGER t1 BEFORE UPDATE ON s.items FOR EACH ROW EXECUTE FUNCTION f()", enabled: "D", comment: "c"},
			{qname: "t2", def: "CREATE TRIGGER t2 ...", inherited: true, enabled: "O"},
		},
		inherits: []string{"s.base"},
	}
	want := `CREATE UNLOGGED TABLE s.items (
    id integer GENERATED ALWAYS AS IDENTITY NOT NULL,
    "Name" text COLLATE pg_catalog."C" DEFAULT 'x'::text NOT NULL,
    total numeric GENERATED ALWAYS AS ((qty * 2)) STORED,
    CONSTRAINT items_pkey PRIMARY KEY (id)
)
INHERITS (s.base)
WITH (fillfactor=70, x='a b');

ALTER TABLE s.items ENABLE ROW LEVEL SECURITY;

CREATE INDEX items_name ON s.items USING btree ("Name");

CREATE TRIGGER t1 BEFORE UPDATE ON s.items FOR EACH ROW EXECUTE FUNCTION f();
ALTER TABLE s.items DISABLE TRIGGER t1;

COMMENT ON TABLE s.items IS 'It''s stock';
COMMENT ON COLUMN s.items."Name" IS 'the name';
COMMENT ON CONSTRAINT items_pkey ON s.items IS 'pk';
COMMENT ON INDEX s.items_name IS 'by name';
COMMENT ON TRIGGER t1 ON s.items IS 'c';
`
	if got := r.ddl(); got != want {
		t.Errorf("table DDL:\n%s\nwant:\n%s", got, want)
	}

	part := &relDef{kind: "r", qname: "s.m_2024", persistence: "p", partOf: "s.m",
		partBound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')", partKey: "LIST (x)",
		cols: []colDef{{qname: "x", typ: "int"}}}
	if got, want := part.ddl(), "CREATE TABLE s.m_2024 PARTITION OF s.m\nFOR VALUES FROM ('2024-01-01') TO ('2025-01-01')\nPARTITION BY LIST (x);\n"; got != want {
		t.Errorf("partition DDL:\n%q\nwant\n%q", got, want)
	}

	ft := &relDef{kind: "f", qname: "s.ft", persistence: "p", server: "srv", ftOptions: []string{"table_name=t'x"},
		cols: []colDef{{qname: "a", typ: "int", local: true}}, comment: "remote"}
	if got, want := ft.ddl(), "CREATE FOREIGN TABLE s.ft (\n    a int\n)\nSERVER srv\nOPTIONS (table_name 't''x');\n\nCOMMENT ON FOREIGN TABLE s.ft IS 'remote';\n"; got != want {
		t.Errorf("foreign DDL:\n%q\nwant\n%q", got, want)
	}

	empty := &relDef{kind: "r", qname: "s.e", persistence: "p"}
	if got := empty.ddl(); got != "CREATE TABLE s.e ();\n" {
		t.Errorf("empty table DDL: %q", got)
	}

	v := &relDef{kind: "v", qname: "s.v", options: []string{"check_option=local"}, viewDef: " SELECT a\n   FROM s.t;"}
	if got, want := v.ddl(), "CREATE VIEW s.v WITH (check_option=local) AS\nSELECT a\n   FROM s.t;\n"; got != want {
		t.Errorf("view DDL: %q, want %q", got, want)
	}
	m := &relDef{kind: "m", qname: "s.m", viewDef: " SELECT 1;", idx: []idxDef{{def: "CREATE INDEX i ON s.m USING btree (a)"}}}
	if got, want := m.ddl(), "CREATE MATERIALIZED VIEW s.m AS\nSELECT 1\nWITH NO DATA;\n\nCREATE INDEX i ON s.m USING btree (a);\n"; got != want {
		t.Errorf("matview DDL: %q, want %q", got, want)
	}
	if !strings.Contains((&relDef{kind: "m", qname: "s.m", populated: true, viewDef: "SELECT 1"}).ddl(), "WITH DATA;") {
		t.Error("populated matview")
	}
}

func TestStatRows(t *testing.T) {
	rows, total := -1.0, "8192 bytes"
	scans := int64(12345)
	got := relStats{rows: &rows, total: &total, seqScan: &scans}.statRows("r")
	want := map[string]string{"Estimated rows": "unknown (not analyzed yet)", "Total size": "8192 bytes", "Table size": "n/a",
		"Last vacuum": "never", "Seq scans": "12,345", "Index scans": "n/a"}
	for _, r := range got {
		if w, ok := want[r.Name]; ok && r.Definition != w {
			t.Errorf("%s = %q, want %q", r.Name, r.Definition, w)
		}
	}
	if len(got) != 10 {
		t.Errorf("%d stat rows", len(got))
	}
	if (relStats{}).statRows("v") != nil {
		t.Error("views have no stats")
	}
}
