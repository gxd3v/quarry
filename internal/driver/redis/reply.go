package redis

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

// table is a command reply laid out as rows. cursor is set for SCAN-family
// replies.
type table struct {
	cols   []driver.Column
	rows   [][]any
	msgs   []driver.Message
	cursor string
}

// verb returns the upper-cased command name, plus the subcommand for
// container commands (CONFIG GET, CLIENT LIST, ...).
func verb(args []string) (cmd, full string) {
	if len(args) == 0 {
		return "", ""
	}
	cmd = strings.ToUpper(args[0])
	full = cmd
	switch cmd {
	case "CONFIG", "CLIENT", "SCRIPT", "FUNCTION", "CLUSTER", "ACL", "MEMORY", "OBJECT",
		"XGROUP", "XINFO", "COMMAND", "SLOWLOG", "LATENCY", "PUBSUB", "MODULE":
		if len(args) > 1 {
			full += " " + strings.ToUpper(args[1])
		}
	}
	return cmd, full
}

func verbOf(args []string) string {
	cmd, _ := verb(args)
	return cmd
}

func hasArg(args []string, flag string) bool {
	for _, a := range args[1:] {
		if strings.EqualFold(a, flag) {
			return true
		}
	}
	return false
}

// tabulate lays out the reply v of the command args (as returned by go-redis:
// nil, string, int64, float64, bool, *big.Int, error elements, []any and,
// with RESP3, map[any]any). The shape depends on the command first and on
// the reply's type second:
//
//   - scalars: one "value" column and row;
//   - hash-like replies (HGETALL, CONFIG GET, RESP3 maps): field, value;
//   - WITHSCORES and ZSCAN: member, score;
//   - KEYS and SCAN: key; SMEMBERS and set algebra: member (sorted);
//   - stream entries: id, then one column per field (XREAD adds stream);
//   - INFO, CLUSTER INFO: section, key, value; CLIENT LIST: one column per field;
//   - other arrays: "#" (the element index; LRANGE counts from its start), value.
//
// Nested replies become driver.JSON.
func tabulate(args []string, v any) table {
	cmd, full := verb(args)
	if v == nil {
		return single(nil)
	}
	switch cmd {
	case "SCAN", "SSCAN", "HSCAN", "ZSCAN":
		if t, ok := scanTable(cmd, args, v); ok {
			return t
		}
	case "KEYS":
		if a, ok := v.([]any); ok {
			return sortRows(flatTable(a, "key"))
		}
	case "SMEMBERS", "SINTER", "SUNION", "SDIFF":
		if a, ok := v.([]any); ok {
			return sortRows(flatTable(a, "member"))
		}
	case "SRANDMEMBER", "SPOP":
		if a, ok := v.([]any); ok {
			return flatTable(a, "member")
		}
	case "INFO":
		if s, ok := v.(string); ok {
			return infoTable(s)
		}
	case "XRANGE", "XREVRANGE", "XCLAIM":
		if a, ok := v.([]any); ok && !hasArg(args, "JUSTID") {
			if t, ok := entriesTable(a); ok {
				return t
			}
		}
	case "XREAD", "XREADGROUP":
		if t, ok := xreadTable(v); ok {
			return t
		}
	case "ZRANGE", "ZREVRANGE", "ZRANGEBYSCORE", "ZREVRANGEBYSCORE", "ZRANDMEMBER",
		"ZUNION", "ZINTER", "ZDIFF":
		if hasArg(args, "WITHSCORES") {
			if t, ok := scoreTable(v); ok {
				return t
			}
		}
	case "ZPOPMIN", "ZPOPMAX":
		if t, ok := scoreTable(v); ok {
			return t
		}
	case "HGETALL", "HELLO":
		if t, ok := pairTable(v, "field", "value"); ok {
			return t
		}
	case "HRANDFIELD":
		if hasArg(args, "WITHVALUES") {
			if t, ok := pairTable(v, "field", "value"); ok {
				return t
			}
		}
	case "LRANGE":
		if a, ok := v.([]any); ok {
			start, _ := strconv.ParseInt(arg(args, 2), 10, 64)
			return arrayTable(a, max(start, 0))
		}
	case "JSON.GET":
		if s, ok := v.(string); ok && json.Valid([]byte(s)) {
			return single(driver.JSON(s))
		}
	}
	switch full {
	case "CONFIG GET", "MEMORY STATS", "XINFO STREAM", "CLIENT TRACKINGINFO", "FUNCTION STATS":
		if t, ok := pairTable(v, "field", "value"); ok {
			return t
		}
	case "CLIENT LIST", "CLIENT INFO":
		if s, ok := v.(string); ok {
			return clientTable(s)
		}
	case "CLUSTER INFO":
		if s, ok := v.(string); ok {
			return infoTable(s)
		}
	}
	switch x := v.(type) {
	case map[any]any:
		t, _ := pairTable(x, "field", "value")
		return t
	case []any:
		return arrayTable(x, 0)
	}
	return single(cell(v))
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func single(v any) table {
	t := table{cols: cols("value"), rows: [][]any{{v}}}
	t.cols[0].Type = colType(t.rows, 0)
	return t
}

func arrayTable(a []any, start int64) table {
	t := table{cols: cols("#", "value")}
	for i, e := range a {
		t.rows = append(t.rows, []any{start + int64(i), cell(e)})
	}
	t.cols[1].Type = colType(t.rows, 1)
	return t
}

func cols(names ...string) []driver.Column {
	out := make([]driver.Column, len(names))
	for i, n := range names {
		typ := "string"
		switch n {
		case "#":
			typ = "integer"
		case "score":
			typ = "double"
		}
		out[i] = driver.Column{Name: n, Type: typ, Nullable: true}
	}
	return out
}

// colType names the type of column i from its values.
func colType(rows [][]any, i int) string {
	typ := ""
	for _, r := range rows {
		var t string
		switch r[i].(type) {
		case nil:
			continue
		case int64:
			t = "integer"
		case float64:
			t = "double"
		case driver.Number:
			t = "number"
		case bool:
			t = "boolean"
		case []byte:
			t = "binary"
		case driver.JSON:
			t = "json"
		default:
			t = "string"
		}
		if typ == "" {
			typ = t
		} else if typ != t {
			return "mixed"
		}
	}
	return cmp.Or(typ, "string")
}

// cell normalises a reply element to a driver value: strings that are not
// valid UTF-8 become []byte, big numbers driver.Number, nested replies JSON,
// error elements (EXEC) "(error) …" text.
func cell(v any) any {
	switch x := v.(type) {
	case nil, int64, float64, bool:
		return x
	case string:
		if utf8.ValidString(x) {
			return x
		}
		return []byte(x)
	case *big.Int:
		return driver.Number(x.String())
	case error:
		return "(error) " + x.Error()
	case []any, map[any]any:
		return toJSON(x)
	case int:
		return int64(x)
	}
	return fmt.Sprint(v)
}

// text renders a scalar reply element as a string.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case error:
		return x.Error()
	}
	return fmt.Sprint(v)
}

// label renders a name for a column header or tree node: as is when it is
// valid UTF-8, quoted with escapes otherwise.
func label(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return Quote(s)
}

func toJSON(v any) driver.JSON {
	b, err := json.Marshal(jsonable(v))
	if err != nil {
		return driver.JSON(strconv.Quote(fmt.Sprint(v)))
	}
	return driver.JSON(b)
}

func jsonable(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonable(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[text(k)] = jsonable(e)
		}
		return out
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return strconv.FormatFloat(x, 'g', -1, 64)
		}
		return x
	case *big.Int:
		return json.Number(x.String())
	case error:
		return "(error) " + x.Error()
	}
	return v
}

// scanTable lays out a [cursor, items] page and reports its cursor.
func scanTable(cmd string, args []string, v any) (table, bool) {
	a, ok := v.([]any)
	if !ok || len(a) != 2 {
		return table{}, false
	}
	items, ok := a[1].([]any)
	if !ok {
		return table{}, false
	}
	var t table
	switch {
	case cmd == "SCAN":
		t = flatTable(items, "key")
	case cmd == "SSCAN":
		t = flatTable(items, "member")
	case cmd == "HSCAN" && hasArg(args, "NOVALUES"):
		t = flatTable(items, "field")
	case cmd == "HSCAN":
		t, ok = pairTable(items, "field", "value")
	case cmd == "ZSCAN":
		t, ok = scoreTable(items)
	}
	if !ok {
		return table{}, false
	}
	t.cursor = text(a[0])
	return t, true
}

func flatTable(items []any, name string) table {
	t := table{cols: cols(name)}
	for _, e := range items {
		t.rows = append(t.rows, []any{cell(e)})
	}
	t.cols[0].Type = colType(t.rows, 0)
	return t
}

func sortRows(t table) table {
	slices.SortFunc(t.rows, func(a, b []any) int { return cmp.Compare(text(a[0]), text(b[0])) })
	return t
}

// pairTable lays out a RESP3 map, a flat [k, v, k, v] array (RESP2) or an
// array of [k, v] pairs as two columns. Map keys are sorted.
func pairTable(v any, k, val string) (table, bool) {
	t := table{cols: cols(k, val)}
	switch x := v.(type) {
	case map[any]any:
		for key, e := range x {
			t.rows = append(t.rows, []any{cell(key), cell(e)})
		}
		t = sortRows(t)
	case []any:
		p, ok := pairs(x)
		if !ok {
			return table{}, false
		}
		for _, e := range p {
			t.rows = append(t.rows, []any{cell(e[0]), cell(e[1])})
		}
	default:
		return table{}, false
	}
	t.cols[0].Type = colType(t.rows, 0)
	t.cols[1].Type = colType(t.rows, 1)
	return t, true
}

// pairs reads [k, v, k, v] or [[k, v], [k, v]].
func pairs(a []any) ([][2]any, bool) {
	if len(a) == 0 {
		return nil, true
	}
	if _, nested := a[0].([]any); nested {
		out := make([][2]any, 0, len(a))
		for _, e := range a {
			p, ok := e.([]any)
			if !ok || len(p) != 2 {
				return nil, false
			}
			out = append(out, [2]any{p[0], p[1]})
		}
		return out, true
	}
	if len(a)%2 != 0 {
		return nil, false
	}
	out := make([][2]any, 0, len(a)/2)
	for i := 0; i < len(a); i += 2 {
		if isAggregate(a[i]) {
			return nil, false
		}
		out = append(out, [2]any{a[i], a[i+1]})
	}
	return out, true
}

func isAggregate(v any) bool {
	switch v.(type) {
	case []any, map[any]any:
		return true
	}
	return false
}

// scoreTable lays out member/score pairs; RESP2 scores arrive as strings.
func scoreTable(v any) (table, bool) {
	a, ok := v.([]any)
	if !ok {
		return table{}, false
	}
	p, ok := pairs(a)
	if !ok {
		return table{}, false
	}
	t := table{cols: cols("member", "score")}
	for _, e := range p {
		t.rows = append(t.rows, []any{cell(e[0]), score(e[1])})
	}
	t.cols[0].Type = colType(t.rows, 0)
	return t, true
}

func score(v any) any {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return cell(v)
}

// fieldSuffix marks a stream field whose name collides with a fixed column.
const fieldSuffix = " (field)"

// streamTable collects stream entries as one row per entry: the fixed
// columns (id, or stream and id) and one column per field name.
type streamTable struct {
	fixed  []string
	fields []string
	index  map[string]int
	rows   [][]any // fixed values, then the values of fields by index
}

func newStreamTable(fixed ...string) *streamTable {
	return &streamTable{fixed: fixed, index: map[string]int{}}
}

func (st *streamTable) column(field any) int {
	name := label(text(field))
	if slices.Contains(st.fixed, name) {
		name += fieldSuffix
	}
	i, ok := st.index[name]
	if !ok {
		i = len(st.fixed) + len(st.fields)
		st.index[name] = i
		st.fields = append(st.fields, name)
	}
	return i
}

// add appends entries [[id, [f, v, ...]], ...]; lead holds the values of
// the fixed columns before id.
func (st *streamTable) add(a []any, lead ...any) bool {
	for _, e := range a {
		if e == nil {
			continue // XCLAIM reports deleted entries as nil
		}
		ent, ok := e.([]any)
		if !ok || len(ent) != 2 {
			return false
		}
		row := append(slices.Clone(lead), cell(ent[0]))
		set := func(f, v any) {
			i := st.column(f)
			for len(row) <= i {
				row = append(row, nil)
			}
			row[i] = cell(v)
		}
		switch fv := ent[1].(type) {
		case nil:
		case []any:
			p, ok := pairs(fv)
			if !ok {
				return false
			}
			for _, kv := range p {
				set(kv[0], kv[1])
			}
		case map[any]any:
			sub, _ := pairTable(fv, "field", "value")
			for _, r := range sub.rows {
				set(r[0], r[1])
			}
		default:
			return false
		}
		st.rows = append(st.rows, row)
	}
	return true
}

func (st *streamTable) table() table {
	t := table{cols: cols(append(slices.Clone(st.fixed), st.fields...)...)}
	width := len(t.cols)
	for _, r := range st.rows {
		for len(r) < width {
			r = append(r, nil)
		}
		t.rows = append(t.rows, r)
	}
	for i := len(st.fixed); i < width; i++ {
		t.cols[i].Type = colType(t.rows, i)
	}
	return t
}

func entriesTable(a []any) (table, bool) {
	st := newStreamTable("id")
	if !st.add(a) {
		return table{}, false
	}
	return st.table(), true
}

// xreadTable lays out XREAD replies: RESP2 [[stream, entries], ...] or a
// RESP3 map stream → entries.
func xreadTable(v any) (table, bool) {
	st := newStreamTable("stream", "id")
	add := func(name, entries any) bool {
		a, ok := entries.([]any)
		return ok && st.add(a, cell(name))
	}
	switch x := v.(type) {
	case map[any]any:
		names := make([]any, 0, len(x))
		for k := range x {
			names = append(names, k)
		}
		slices.SortFunc(names, func(a, b any) int { return cmp.Compare(text(a), text(b)) })
		for _, n := range names {
			if !add(n, x[n]) {
				return table{}, false
			}
		}
	case []any:
		for _, e := range x {
			p, ok := e.([]any)
			if !ok || len(p) != 2 || !add(p[0], p[1]) {
				return table{}, false
			}
		}
	default:
		return table{}, false
	}
	return st.table(), true
}

// infoTable parses INFO output ("# Section" headers, "key:value" lines).
func infoTable(s string) table {
	t := table{cols: cols("section", "key", "value")}
	section := ""
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		t.rows = append(t.rows, []any{section, k, infoValue(v)})
	}
	t.cols[2].Type = colType(t.rows, 2)
	return t
}

func infoValue(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	return v
}

// clientTable parses CLIENT LIST / CLIENT INFO: one client per line of
// space-separated key=value pairs.
func clientTable(s string) table {
	var (
		names []string
		index = map[string]int{}
		recs  []map[string]string
	)
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rec := map[string]string{}
		for _, kv := range strings.Fields(line) {
			k, v, _ := strings.Cut(kv, "=")
			if _, ok := index[k]; !ok {
				index[k] = len(names)
				names = append(names, k)
			}
			rec[k] = v
		}
		recs = append(recs, rec)
	}
	t := table{cols: cols(names...)}
	for _, rec := range recs {
		row := make([]any, len(names))
		for i, n := range names {
			if v, ok := rec[n]; ok {
				row[i] = infoValue(v)
			}
		}
		t.rows = append(t.rows, row)
	}
	for i := range t.cols {
		t.cols[i].Type = colType(t.rows, i)
	}
	return t
}
