package export

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

// sliceSource serves rows in pages of at most per (or the requested n).
func sliceSource(rows [][]any, per int) Source {
	return func(_ context.Context, n int) ([][]any, error) {
		if per > 0 {
			n = min(n, per)
		}
		n = min(n, len(rows))
		page := rows[:n]
		rows = rows[n:]
		return page, nil
	}
}

func export(t *testing.T, cols []driver.Column, rows [][]any, o Options) string {
	t.Helper()
	var sb strings.Builder
	n, err := Write(t.Context(), &sb, cols, sliceSource(rows, 0), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(rows)) {
		t.Fatalf("rows = %d, want %d", n, len(rows))
	}
	return sb.String()
}

func columns(names ...string) []driver.Column {
	cols := make([]driver.Column, len(names))
	for i, n := range names {
		cols[i] = driver.Column{Name: n}
	}
	return cols
}

var ts = time.Date(2024, 1, 2, 3, 4, 5, 123e6, time.UTC)

func check(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestText(t *testing.T) {
	plus2 := time.FixedZone("", 2*3600)
	for _, c := range []struct {
		v    any
		want string
	}{
		{nil, ""},
		{true, "true"},
		{int64(-7), "-7"},
		{1.5, "1.5"},
		{1e21, "1e+21"},
		{math.NaN(), "NaN"},
		{math.Inf(-1), "-Infinity"},
		{driver.Number("12.50"), "12.50"},
		{"héllo\n", "héllo\n"},
		{[]byte{0xde, 0xad}, `\xdead`},
		{ts, "2024-01-02 03:04:05.123"},
		{time.Date(2024, 1, 2, 0, 0, 0, 0, plus2), "2024-01-02 00:00:00+02:00"},
		{driver.JSON(`{"a": 1}`), `{"a": 1}`},
		{[]any{"a b", nil, "NULL", "", []any{int64(1), `q"\`}}, `{"a b",NULL,"NULL","",{1,"q\"\\"}}`},
	} {
		if got := Text(c.v); got != c.want {
			t.Errorf("Text(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestJSONValue(t *testing.T) {
	v := []any{nil, true, int64(3), math.NaN(), math.Inf(1), 2.5, driver.Number("1.10"), driver.Number("NaN"),
		[]byte{1}, ts, driver.JSON("{\n \"a\": [1, 2]\n}"), driver.JSON("{bad"), []any{int64(1), []any{"x"}}}
	got := make([]any, len(v))
	for i, e := range v {
		got[i] = JSONValue(e)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	check(t, string(b), `[null,true,3,null,null,2.5,1.10,"NaN","\\x01","2024-01-02T03:04:05.123Z",{"a":[1,2]},"{bad",[1,["x"]]]`)
}

func TestSQLLiteral(t *testing.T) {
	plus2 := time.FixedZone("", 2*3600)
	tz := time.Date(2024, 1, 2, 3, 4, 5, 0, plus2)
	for _, c := range []struct {
		v                any
		generic, pg, ora string
	}{
		{nil, "NULL", "NULL", "NULL"},
		{true, "TRUE", "TRUE", "1"},
		{false, "FALSE", "FALSE", "0"},
		{int64(42), "42", "42", "42"},
		{0.25, "0.25", "0.25", "0.25"},
		{math.NaN(), "'NaN'", "'NaN'", "BINARY_DOUBLE_NAN"},
		{math.Inf(-1), "'-Infinity'", "'-Infinity'", "-BINARY_DOUBLE_INFINITY"},
		{driver.Number("-1.5e3"), "-1.5e3", "-1.5e3", "-1.5e3"},
		{driver.Number("NaN"), "'NaN'", "'NaN'", "'NaN'"},
		{"it's", "'it''s'", "'it''s'", "'it''s'"},
		{[]byte{0xca, 0xfe}, `'\xcafe'`, `'\xcafe'`, "HEXTORAW('cafe')"},
		{ts, "'2024-01-02 03:04:05.123'", "'2024-01-02 03:04:05.123+00:00'", "TIMESTAMP '2024-01-02 03:04:05.123'"},
		{tz, "'2024-01-02 03:04:05+02:00'", "'2024-01-02 03:04:05+02:00'", "TIMESTAMP '2024-01-02 03:04:05 +02:00'"},
		{[]any{int64(1), "a'b"}, "'{1,a''b}'", "'{1,a''b}'", "'{1,a''b}'"},
		{driver.JSON(`{"k":"v"}`), `'{"k":"v"}'`, `'{"k":"v"}'`, `'{"k":"v"}'`},
	} {
		for d, want := range map[string]string{"": c.generic, "postgres": c.pg, "oracle": c.ora} {
			if got := SQLLiteral(c.v, d); got != want {
				t.Errorf("SQLLiteral(%#v, %q) = %s, want %s", c.v, d, got, want)
			}
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	for _, c := range []struct{ in, pg, ora string }{
		{"id", "id", `"id"`},
		{"ID", `"ID"`, "ID"},
		{"order_2", "order_2", `"order_2"`},
		{"Mixed", `"Mixed"`, `"Mixed"`},
		{`a"b`, `"a""b"`, `"a""b"`},
		{"2x", `"2x"`, `"2x"`},
		{"", `""`, `""`},
		{"A#1", `"A#1"`, "A#1"},
	} {
		if got := QuoteIdent(c.in, "postgres"); got != c.pg {
			t.Errorf("QuoteIdent(%q, pg) = %s, want %s", c.in, got, c.pg)
		}
		if got := QuoteIdent(c.in, "oracle"); got != c.ora {
			t.Errorf("QuoteIdent(%q, oracle) = %s, want %s", c.in, got, c.ora)
		}
	}
}

// tricky has one row of awkward values and one of NULLs and specials.
var (
	trickyCols = columns("id", "name", "amount", "data", "tags", "doc")
	trickyRows = [][]any{
		{int64(1), "say \"hi\", ok\nbye", driver.Number("12.50"), []byte{0xde, 0xad}, []any{"a b", nil, []any{int64(1), int64(2)}}, driver.JSON("{\"a\": [1,\n 2]}")},
		{nil, "héllo|世界\ttab", math.NaN(), nil, []any{}, nil},
		{int64(3), ""}, // short row: missing values are NULL
	}
)

func TestCSV(t *testing.T) {
	got := export(t, trickyCols, trickyRows, DefaultOptions(CSV))
	check(t, got, "id,name,amount,data,tags,doc\r\n"+
		"1,\"say \"\"hi\"\", ok\nbye\",12.50,\\xdead,\"{\"\"a b\"\",NULL,{1,2}}\",\"{\"\"a\"\": [1,\n 2]}\"\r\n"+
		",héllo|世界\ttab,NaN,,{},\r\n"+
		"3,\"\",,,,\r\n")
}

func TestCSVOptions(t *testing.T) {
	cols := columns("a", "b;c")
	rows := [][]any{{"x;y", nil}, {"NULL", int64(2)}}
	o := DefaultOptions(CSV)
	o.Delimiter, o.NullText, o.CRLF, o.BOM = ';', "NULL", false, true
	check(t, export(t, cols, rows, o), "\ufeffa;\"b;c\"\n\"x;y\";NULL\n\"NULL\";2\n")

	o = DefaultOptions(CSV)
	o.QuoteAll, o.Header = true, false
	check(t, export(t, cols, rows, o), "\"x;y\",\r\n\"NULL\",\"2\"\r\n")

	o.Header = true
	check(t, export(t, cols, nil, o), "\"a\",\"b;c\"\r\n")

	o.Delimiter = '"'
	if _, err := Write(t.Context(), &strings.Builder{}, cols, sliceSource(rows, 0), o, nil); err == nil {
		t.Error("quote delimiter accepted")
	}
}

func TestTSV(t *testing.T) {
	o := DefaultOptions(TSV)
	o.NullText = `\N`
	got := export(t, trickyCols, trickyRows, o)
	check(t, got, "id\tname\tamount\tdata\ttags\tdoc\n"+
		"1\t\"say \"\"hi\"\", ok\nbye\"\t12.50\t\\xdead\t\"{\"\"a b\"\",NULL,{1,2}}\"\t\"{\"\"a\"\": [1,\n 2]}\"\n"+
		"\\N\t\"héllo|世界\ttab\"\tNaN\t\\N\t{}\t\\N\n"+
		"3\t\t\\N\t\\N\t\\N\t\\N\n")
}

func TestJSON(t *testing.T) {
	cols := append(columns("id", "name", "id"), trickyCols[3:]...)
	got := export(t, cols, trickyRows, DefaultOptions(JSON))
	check(t, got, "[\n"+
		`{"id":1,"name":"say \"hi\", ok\nbye","id_2":12.50,"data":"\\xdead","tags":["a b",null,[1,2]],"doc":{"a":[1,2]}},`+"\n"+
		`{"id":null,"name":"héllo|世界\ttab","id_2":null,"data":null,"tags":[],"doc":null},`+"\n"+
		`{"id":3,"name":"","id_2":null,"data":null,"tags":null,"doc":null}`+"\n]\n")
	if !jsontext.Value(got).IsValid() {
		t.Error("invalid JSON")
	}
	check(t, export(t, cols, nil, DefaultOptions(JSON)), "[]\n")
}

func TestNDJSON(t *testing.T) {
	o := DefaultOptions(NDJSON)
	o.CRLF = true
	got := export(t, trickyCols, trickyRows[:2], o)
	check(t, got, `{"id":1,"name":"say \"hi\", ok\nbye","amount":12.50,"data":"\\xdead","tags":["a b",null,[1,2]],"doc":{"a":[1,2]}}`+"\r\n"+
		`{"id":null,"name":"héllo|世界\ttab","amount":null,"data":null,"tags":[],"doc":null}`+"\r\n")
	check(t, export(t, trickyCols, nil, o), "")

	odd := [][]any{{driver.JSON(`{"a":1,"a":2}`)}, {driver.JSON("{bad\n")}, {driver.JSON("\"\xff\"")}}
	check(t, export(t, columns("j"), odd, DefaultOptions(NDJSON)), `{"j":{"a":1,"a":2}}`+"\n"+`{"j":"{bad\n"}`+"\n"+`{"j":"�"}`+"\n")
}

func TestSQLInsert(t *testing.T) {
	cols := columns("id", "Name", "at")
	rows := [][]any{{int64(1), "o'k", ts}, {int64(2), nil, nil}, {int64(3), "x", nil}, {int64(4), "y", nil}}
	o := DefaultOptions(SQLInsert)
	o.Table = "app.items"
	check(t, export(t, cols, rows[:2], o), `INSERT INTO app.items (id, "Name", at) VALUES (1, 'o''k', '2024-01-02 03:04:05.123');`+"\n"+
		`INSERT INTO app.items (id, "Name", at) VALUES (2, NULL, NULL);`+"\n")

	o.BatchRows = 3
	check(t, export(t, cols, rows, o), `INSERT INTO app.items (id, "Name", at) VALUES`+"\n"+
		`  (1, 'o''k', '2024-01-02 03:04:05.123'),`+"\n"+
		"  (2, NULL, NULL),\n"+
		"  (3, 'x', NULL);\n"+
		`INSERT INTO app.items (id, "Name", at) VALUES`+"\n"+
		"  (4, 'y', NULL);\n")

	o.Dialect, o.Table = "oracle", "T"
	check(t, export(t, cols, rows, o), "INSERT ALL\n"+
		`  INTO T ("id", "Name", "at") VALUES (1, 'o''k', TIMESTAMP '2024-01-02 03:04:05.123')`+"\n"+
		`  INTO T ("id", "Name", "at") VALUES (2, NULL, NULL)`+"\n"+
		`  INTO T ("id", "Name", "at") VALUES (3, 'x', NULL)`+"\n"+
		"SELECT 1 FROM DUAL;\n"+
		"INSERT ALL\n"+
		`  INTO T ("id", "Name", "at") VALUES (4, 'y', NULL)`+"\n"+
		"SELECT 1 FROM DUAL;\n")

	check(t, export(t, cols, nil, o), "")
}

func TestMarkdown(t *testing.T) {
	cols := columns("n", "text|pipe")
	rows := [][]any{{int64(1), `a|b\` + "\nc"}, {nil, "世界"}, {2.5, nil}}
	check(t, export(t, cols, rows, DefaultOptions(Markdown)), ""+
		"|    n | text\\|pipe  |\n"+
		"| ---: | ----------- |\n"+
		`|    1 | a\|b\\<br>c |`+"\n"+
		"| NULL | 世界        |\n"+
		"|  2.5 | NULL        |\n")

	o := DefaultOptions(Markdown)
	o.Header = false
	check(t, export(t, cols, nil, o), "|     |     |\n| --- | --- |\n")
}

func TestMarkdownLaterPagesOverflow(t *testing.T) {
	rows := make([][]any, pageSize+1)
	for i := range rows {
		rows[i] = []any{"ab"}
	}
	rows[pageSize] = []any{"longer"}
	got := export(t, columns("c"), rows, DefaultOptions(Markdown))
	if !strings.HasSuffix(got, "| ab  |\n| longer |\n") {
		t.Errorf("unexpected tail: %q", got[len(got)-30:])
	}
}

func TestFormatFromExt(t *testing.T) {
	for path, want := range map[string]Format{
		"a.csv": CSV, "a.TSV": TSV, "dir.x/a.json": JSON, "a.ndjson": NDJSON, "a.jsonl": NDJSON,
		"a.sql": SQLInsert, "a.md": Markdown,
	} {
		if got, ok := FormatFromExt(path); !ok || got != want {
			t.Errorf("FormatFromExt(%q) = %v, %v", path, got, ok)
		}
	}
	for _, path := range []string{"a.txt", "csv", "a"} {
		if _, ok := FormatFromExt(path); ok {
			t.Errorf("FormatFromExt(%q) ok", path)
		}
	}
}

func TestProgress(t *testing.T) {
	rows := make([][]any, 2500)
	for i := range rows {
		rows[i] = []any{int64(i)}
	}
	var calls []int64
	n, err := Write(t.Context(), &strings.Builder{}, columns("i"), sliceSource(rows, 300), DefaultOptions(CSV),
		func(r int64) { calls = append(calls, r) })
	if err != nil || n != 2500 {
		t.Fatal(n, err)
	}
	check(t, fmt.Sprint(calls), "[1200 2400 2500]")
}

// cancelAfter is an endless source that cancels after pages pages.
func cancelAfter(pages int, cancel context.CancelFunc) Source {
	page := [][]any{{int64(1), "x"}}
	return func(ctx context.Context, n int) ([][]any, error) {
		if pages--; pages == 0 {
			cancel()
		}
		return page, nil
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.csv")
	n, err := WriteFile(t.Context(), path, trickyCols[:2], sliceSource(trickyRows, 0), DefaultOptions(CSV), nil)
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	check(t, string(b), "id,name\r\n1,\"say \"\"hi\"\", ok\nbye\"\r\n,héllo|世界\ttab\r\n3,\"\"\r\n")

	// A cancelled export leaves the previous file and no temporary.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = WriteFile(ctx, path, trickyCols[:2], cancelAfter(5, cancel), DefaultOptions(CSV), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(b) {
		t.Error("cancelled export replaced the file")
	}
	ctx, cancel = context.WithCancel(t.Context())
	fresh := filepath.Join(dir, "fresh.ndjson")
	if _, err := WriteFile(ctx, fresh, trickyCols[:2], cancelAfter(3, cancel), DefaultOptions(NDJSON), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "out.csv" {
		t.Errorf("dir has %v", entries)
	}

	srcErr := errors.New("boom")
	failing := func(context.Context, int) ([][]any, error) { return nil, srcErr }
	if _, err := WriteFile(t.Context(), fresh, nil, failing, DefaultOptions(JSON), nil); !errors.Is(err, srcErr) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("dir has %v", entries)
	}
}

func TestIsNumberLiteral(t *testing.T) {
	for s, want := range map[string]bool{
		"0": true, "-0.5e-3": true, "12.50": true, "1E+9": true,
		"": false, "-": false, "01": false, "1.": false, ".5": false, "1e": false, "+1": false, "NaN": false, "1 ": false,
	} {
		if got := isNumberLiteral(s); got != want {
			t.Errorf("isNumberLiteral(%q) = %v", s, got)
		}
	}
}
