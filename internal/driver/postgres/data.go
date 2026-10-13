package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gxd3v/quarry/internal/driver"
)

var (
	_ driver.DataViewer = (*conn)(nil)
	_ driver.Editable   = (*conn)(nil)

	errReadOnly = errors.New("connection is read-only: changes cannot be saved")
)

// relation resolves a table, view or materialized view node
// ([db, schema, folder, name]) to its folder, schema and name.
func relation(target *driver.Node) (folder, schema, name string, err error) {
	if target == nil || len(target.Path) != 4 {
		return "", "", "", errors.New("not a table or view")
	}
	p := target.Path
	switch p[2] {
	case folderTables, folderViews, folderMatviews:
		return p[2], p[1], p[3], nil
	}
	return "", "", "", fmt.Errorf("%s is not a table or view", p[3])
}

// DataQuery builds the statement behind a table's data view:
//
//	SELECT * FROM "schema"."table" WHERE (<filter>) ORDER BY "col" DESC, ...
//
// filter is a SQL boolean expression inserted verbatim (a leading WHERE and
// trailing semicolons are dropped). Each orderBy term is a column name,
// optionally followed by ASC or DESC; the name is always quoted as one
// identifier, so it is matched exactly (case-sensitively) and cannot inject
// SQL. A name containing whitespace or double quotes must be given in
// double-quoted SQL form ("my col" DESC); any other suffix is rejected.
// There is no LIMIT: the UI pages through the streamed result.
func (c *conn) DataQuery(target *driver.Node, filter string, orderBy []string) (string, error) {
	_, schema, name, err := relation(target)
	if err != nil {
		return "", err
	}
	return dataQuery(schema, name, filter, orderBy)
}

func dataQuery(schema, name, filter string, orderBy []string) (string, error) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(pgx.Identifier{schema, name}.Sanitize())
	if f := trimFilter(filter); f != "" {
		b.WriteString(" WHERE (")
		b.WriteString(f)
		if strings.Contains(f, "--") {
			// A trailing line comment would swallow the parenthesis.
			b.WriteByte('\n')
		}
		b.WriteByte(')')
	}
	for i, term := range orderBy {
		t, err := sortTerm(term)
		if err != nil {
			return "", err
		}
		if i == 0 {
			b.WriteString(" ORDER BY ")
		} else {
			b.WriteString(", ")
		}
		b.WriteString(t)
	}
	return b.String(), nil
}

func trimFilter(f string) string {
	f = strings.TrimSpace(f)
	for strings.HasSuffix(f, ";") {
		f = strings.TrimSpace(strings.TrimSuffix(f, ";"))
	}
	if len(f) > 6 && strings.EqualFold(f[:5], "where") && isSpace(f[5]) {
		f = strings.TrimSpace(f[6:])
	}
	return f
}

// sortTerm turns `col`, `col ASC`, `col DESC` or `"my col" DESC` into a
// quoted ORDER BY term.
func sortTerm(term string) (string, error) {
	t := strings.TrimSpace(term)
	dir := ""
	if i := strings.LastIndexAny(t, " \t\r\n"); i >= 0 {
		switch strings.ToUpper(t[i+1:]) {
		case "DESC":
			dir = " DESC"
			t = strings.TrimSpace(t[:i])
		case "ASC":
			t = strings.TrimSpace(t[:i])
		}
	}
	col, ok := sortColumn(t)
	if !ok {
		return "", fmt.Errorf("invalid sort term %q: want a column name optionally followed by ASC or DESC", term)
	}
	return pgx.Identifier{col}.Sanitize() + dir, nil
}

func sortColumn(t string) (string, bool) {
	if t == "" || strings.ContainsRune(t, 0) {
		return "", false
	}
	if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
		inner := t[1 : len(t)-1]
		if inner == "" || strings.Contains(strings.ReplaceAll(inner, `""`, ""), `"`) {
			return "", false
		}
		return strings.ReplaceAll(inner, `""`, `"`), true
	}
	if strings.ContainsAny(t, " \t\r\n\"") {
		return "", false
	}
	return t, true
}

// rowKeyQuery returns the relation kind, its primary key columns (in key
// order) and the columns of its best unique index whose columns are all
// NOT NULL: no predicate, no expressions, fewest columns, then by name.
const rowKeyQuery = `
SELECT c.relkind::text,
       (SELECT array_agg(a.attname::text ORDER BY k.ord)
        FROM pg_constraint p
        CROSS JOIN LATERAL unnest(p.conkey) WITH ORDINALITY k(attnum, ord)
        JOIN pg_attribute a ON a.attrelid = p.conrelid AND a.attnum = k.attnum
        WHERE p.conrelid = c.oid AND p.contype = 'p'),
       (SELECT u.cols
        FROM (SELECT ir.relname, array_agg(a.attname::text ORDER BY k.ord) AS cols,
                     bool_and(a.attnotnull) AS notnull, count(*) AS n
              FROM pg_index i
              JOIN pg_class ir ON ir.oid = i.indexrelid
              CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY k(attnum, ord)
              JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
              WHERE i.indrelid = c.oid AND i.indisunique AND i.indisvalid
                AND i.indpred IS NULL AND i.indexprs IS NULL AND k.ord <= i.indnkeyatts
              GROUP BY i.indexrelid, ir.relname) u
        WHERE u.notnull
        ORDER BY u.n, u.relname
        LIMIT 1)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

// RowKey returns the primary key columns of a table, or else the columns of
// its first unique, non-partial, all-NOT-NULL index, or else nil. Views,
// materialized views and foreign tables return nil: their rows cannot be
// identified reliably (ctid is not stable enough to edit by).
func (c *conn) RowKey(ctx context.Context, target *driver.Node) ([]string, error) {
	folder, schema, name, err := relation(target)
	if err != nil {
		return nil, err
	}
	if folder != folderTables {
		return nil, nil
	}
	var (
		kind     string
		pk, uniq []string
		found    bool
	)
	err = c.list(ctx, target.Path[0], rowKeyQuery, []any{schema, name}, func(r pgx.Rows) error {
		found = true
		return r.Scan(&kind, &pk, &uniq)
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("table %s.%s does not exist", schema, name)
	}
	switch {
	case kind != "r" && kind != "p":
		return nil, nil
	case len(pk) > 0:
		return pk, nil
	case len(uniq) > 0:
		return uniq, nil
	}
	return nil, nil
}

// colMeta is what Apply needs to know about a column.
type colMeta struct {
	name      string
	typ       string // format_type without typmod, see editColumnsQuery
	identity  string // '', 'a' (always), 'd' (by default)
	generated string // '', 's' (stored), 'v' (virtual)
}

// editColumnsQuery lists a relation's columns. Types are formatted without
// their modifier (typmod -1: varchar rather than varchar(20), bpchar rather
// than character): an explicit cast to varchar(20) would silently truncate,
// while the assignment to the column enforces the limit with an error.
const editColumnsQuery = `
SELECT c.relkind::text, a.attname::text, format_type(a.atttypid, -1), a.attidentity::text, a.attgenerated::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY a.attnum`

// Apply saves edits to a table in one transaction, on a dedicated
// connection to the table's database that is closed afterwards (user
// sessions and their transactions are never involved).
//
// Values and keys are strings as typed by the user, nil for NULL, or values
// as decoded by this driver (int64, time.Time, driver.Number, []any, ...).
// They are sent as text parameters cast to the column's type, so the server
// parses them exactly as it would literals.
//
//   - OpUpdate sets Values on the row matched by Key; it must match exactly
//     one row.
//   - OpInsert inserts Values; nil values are inserted as explicit NULLs,
//     columns not in Values get their defaults. Generated and GENERATED
//     ALWAYS identity columns are always omitted, BY DEFAULT identity
//     columns when nil.
//   - OpDelete deletes the row matched by Key; it must match exactly one row.
//
// A NULL key value matches with IS NULL. If any change fails, or an update
// or delete does not match exactly one row (the row changed or was deleted
// concurrently), everything is rolled back. Server errors are returned as
// *driver.QueryError whose Msg says which change failed.
func (c *conn) Apply(ctx context.Context, target *driver.Node, changes []driver.RowChange) error {
	if c.readOnly {
		return errReadOnly
	}
	if c.closed.Load() {
		return errClosed
	}
	folder, schema, name, err := relation(target)
	if err != nil {
		return err
	}
	if folder != folderTables {
		return fmt.Errorf("%s.%s is not a table: its rows cannot be edited", schema, name)
	}
	if len(changes) == 0 {
		return nil
	}
	pc, err := c.connect(ctx, target.Path[0], nil, nil)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		pc.Close(cctx)
	}()

	tx, err := pc.Begin(ctx)
	if err != nil {
		return queryError(err, "")
	}
	defer func() {
		// No-op after a successful commit.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		tx.Rollback(rctx)
	}()

	cols, err := editColumns(ctx, tx, schema, name)
	if err != nil {
		return err
	}
	inserts := 0
	for i, ch := range changes {
		if ch.Op == driver.OpInsert {
			inserts++
		}
		st, err := buildChange(pgx.Identifier{schema, name}.Sanitize(), cols, ch, inserts)
		if err != nil {
			return fmt.Errorf("change %d: %w", i+1, err)
		}
		if st.sql == "" {
			continue
		}
		tag, err := tx.Exec(ctx, st.sql, st.args...)
		if err != nil {
			return changeError(err, st.label)
		}
		if ch.Op != driver.OpInsert {
			switch n := tag.RowsAffected(); {
			case n == 0:
				return fmt.Errorf("row %s changed or was deleted by someone else; no changes were saved", st.key)
			case n > 1:
				return fmt.Errorf("row %s is not unique (it matches %d rows); no changes were saved", st.key, n)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return changeError(err, "saving changes")
	}
	return nil
}

func editColumns(ctx context.Context, tx pgx.Tx, schema, name string) ([]colMeta, error) {
	rows, err := tx.Query(ctx, editColumnsQuery, schema, name)
	if err != nil {
		return nil, queryError(err, "")
	}
	defer rows.Close()
	var (
		cols []colMeta
		kind string
	)
	for rows.Next() {
		var m colMeta
		if err := rows.Scan(&kind, &m.name, &m.typ, &m.identity, &m.generated); err != nil {
			return nil, queryError(err, "")
		}
		cols = append(cols, m)
	}
	if err := rows.Err(); err != nil {
		return nil, queryError(err, "")
	}
	if cols == nil {
		return nil, fmt.Errorf("table %s.%s does not exist", schema, name)
	}
	if kind != "r" && kind != "p" && kind != "f" {
		return nil, fmt.Errorf("%s.%s is not a table: its rows cannot be edited", schema, name)
	}
	return cols, nil
}

// changeError names the failed change in a server error.
func changeError(err error, label string) error {
	qe, ok := queryError(err, "").(*driver.QueryError)
	if !ok {
		return fmt.Errorf("%s: %w", label, err)
	}
	q := *qe
	q.Msg = label + ": " + q.Msg
	q.Position = -1
	return &q
}

// change is one generated DML statement.
type change struct {
	sql   string
	args  []any
	label string // "updating row id=42"
	key   string // "id=42"
}

// buildChange generates the statement for ch on table (already quoted).
// cols lists the table's columns in order; insertNo numbers inserts for
// error messages. An update without values yields an empty statement.
func buildChange(table string, cols []colMeta, ch driver.RowChange, insertNo int) (change, error) {
	var st change
	param := func(v any, typ string) (string, error) {
		s, ok, err := textValue(v)
		if err != nil {
			return "", err
		}
		st.args = append(st.args, pgtype.Text{String: s, Valid: ok})
		return fmt.Sprintf("$%d::text::%s", len(st.args), typ), nil
	}
	values, err := ordered(cols, ch.Values)
	if err != nil {
		return st, err
	}

	if ch.Op == driver.OpInsert {
		st.label = fmt.Sprintf("inserting new row %d", insertNo)
		var names, params []string
		for _, col := range values {
			v := ch.Values[col.name]
			if col.generated != "" || col.identity == "a" || col.identity == "d" && v == nil {
				continue
			}
			p, err := param(v, col.typ)
			if err != nil {
				return st, fmt.Errorf("column %s: %w", col.name, err)
			}
			names = append(names, pgx.Identifier{col.name}.Sanitize())
			params = append(params, p)
		}
		if len(names) == 0 {
			st.sql = "INSERT INTO " + table + " DEFAULT VALUES"
		} else {
			st.sql = "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(params, ", ") + ")"
		}
		return st, nil
	}

	keys, err := ordered(cols, ch.Key)
	if err != nil {
		return st, err
	}
	if len(keys) == 0 {
		return st, errors.New("missing row key")
	}
	st.key = keyDesc(keys, ch.Key)
	var b strings.Builder
	switch ch.Op {
	case driver.OpUpdate:
		if len(values) == 0 {
			return change{}, nil
		}
		st.label = "updating row " + st.key
		b.WriteString("UPDATE " + table + " SET ")
		for i, col := range values {
			p, err := param(ch.Values[col.name], col.typ)
			if err != nil {
				return st, fmt.Errorf("column %s: %w", col.name, err)
			}
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pgx.Identifier{col.name}.Sanitize() + " = " + p)
		}
	case driver.OpDelete:
		st.label = "deleting row " + st.key
		b.WriteString("DELETE FROM " + table)
	default:
		return st, fmt.Errorf("unknown operation %d", ch.Op)
	}
	b.WriteString(" WHERE ")
	for i, col := range keys {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(pgx.Identifier{col.name}.Sanitize())
		v := ch.Key[col.name]
		if v == nil {
			b.WriteString(" IS NULL")
			continue
		}
		p, err := param(v, col.typ)
		if err != nil {
			return st, fmt.Errorf("key column %s: %w", col.name, err)
		}
		b.WriteString(" = " + p)
	}
	st.sql = b.String()
	return st, nil
}

// ordered returns the columns named in m, in table order.
func ordered(cols []colMeta, m map[string]any) ([]colMeta, error) {
	var out []colMeta
	for _, col := range cols {
		if _, ok := m[col.name]; ok {
			out = append(out, col)
		}
	}
	if len(out) != len(m) {
		var unknown []string
		for k := range m {
			if !slices.ContainsFunc(cols, func(c colMeta) bool { return c.name == k }) {
				unknown = append(unknown, strconv.Quote(k))
			}
		}
		slices.Sort(unknown)
		return nil, fmt.Errorf("unknown column %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

// keyDesc renders a row key for messages: id=42, code='x', parent=NULL.
func keyDesc(keys []colMeta, m map[string]any) string {
	parts := make([]string, len(keys))
	for i, col := range keys {
		v := m[col.name]
		var s string
		switch x := v.(type) {
		case nil:
			s = "NULL"
		case string:
			s = x
			if x == "" || strings.ContainsAny(x, " \t\r\n,'=") {
				s = literal(x)
			}
		default:
			t, _, err := textValue(v)
			if err != nil {
				t = fmt.Sprint(v)
			}
			s = t
		}
		parts[i] = col.name + "=" + s
	}
	return strings.Join(parts, ", ")
}

// textValue renders a value in PostgreSQL's text input format; ok is false
// for NULL. Strings pass through verbatim.
func textValue(v any) (s string, ok bool, err error) {
	switch x := v.(type) {
	case nil:
		return "", false, nil
	case string:
		return x, true, nil
	case driver.Number:
		return string(x), true, nil
	case driver.JSON:
		return string(x), true, nil
	case bool:
		return strconv.FormatBool(x), true, nil
	case int64:
		return strconv.FormatInt(x, 10), true, nil
	case int:
		return strconv.Itoa(x), true, nil
	case int32:
		return strconv.FormatInt(int64(x), 10), true, nil
	case float64:
		return formatFloat(x, 64), true, nil
	case float32:
		return formatFloat(float64(x), 32), true, nil
	case []byte:
		return `\x` + hex.EncodeToString(x), true, nil
	case time.Time:
		return x.Format("2006-01-02 15:04:05.999999999Z07:00"), true, nil
	case []any:
		var b strings.Builder
		if err := arrayLiteral(&b, x); err != nil {
			return "", false, err
		}
		return b.String(), true, nil
	case fmt.Stringer:
		return x.String(), true, nil
	}
	return "", false, fmt.Errorf("unsupported value type %T", v)
}

func formatFloat(f float64, bits int) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, bits)
}

// arrayLiteral writes a PostgreSQL array literal: {1,"a b",NULL,{2,3}}.
func arrayLiteral(b *strings.Builder, a []any) error {
	b.WriteByte('{')
	for i, e := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		if sub, ok := e.([]any); ok {
			if err := arrayLiteral(b, sub); err != nil {
				return err
			}
			continue
		}
		s, ok, err := textValue(e)
		if err != nil {
			return err
		}
		if !ok {
			b.WriteString("NULL")
			continue
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return nil
}
