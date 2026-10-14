package oracle

import (
	"context"
	sqldriver "database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

var (
	_ driver.DataViewer = (*conn)(nil)
	_ driver.Editable   = (*conn)(nil)

	errReadOnly = errors.New("connection is read-only: changes cannot be saved")
)

// rowIDKey is the key RowKey returns for tables without a usable key; the
// data view then selects the ROWID pseudo-column under this name.
const rowIDKey = "ROWID"

// relation resolves a table, view or materialized view node
// ([schema, folder, name]) to its folder, schema and name.
func relation(target *driver.Node) (folder, schema, name string, err error) {
	if target == nil || len(target.Path) != 3 {
		return "", "", "", errors.New("not a table or view")
	}
	p := target.Path
	if strings.ContainsAny(p[0]+p[2], "\"\x00") {
		return "", "", "", fmt.Errorf("invalid object name %q", p[0]+"."+p[2])
	}
	switch p[1] {
	case folderTables, folderViews, folderMatviews:
		return p[1], p[0], p[2], nil
	}
	return "", "", "", fmt.Errorf("%s is not a table or view", p[2])
}

// DataQuery builds the statement behind a table's data view:
//
//	SELECT * FROM "SCHEMA"."TABLE" WHERE (<filter>) ORDER BY "COL" DESC, ...
//
// For a table without a primary or unique key the ROWID is selected too, as
// the last column named ROWID, so that rows can be edited (see RowKey); this
// needs one catalog round trip.
//
// filter is a SQL condition inserted verbatim (a leading WHERE and trailing
// semicolons are dropped). Each orderBy term is a column name, optionally
// followed by ASC or DESC; the name is always quoted as one identifier, so
// it is matched exactly (case-sensitively: Oracle stores unquoted names in
// upper case) and cannot inject SQL. A name containing whitespace or double
// quotes must be given in double-quoted SQL form ("my col" DESC); any other
// suffix is rejected. There is no row limit: the UI pages through the
// streamed result.
func (c *conn) DataQuery(target *driver.Node, filter string, orderBy []string) (string, error) {
	folder, schema, name, err := relation(target)
	if err != nil {
		return "", err
	}
	rowID := false
	if folder == folderTables {
		ctx, cancel := context.WithTimeout(context.Background(), cancelDeadline)
		key, err := c.rowKey(ctx, schema, name)
		cancel()
		rowID = err == nil && slices.Equal(key, []string{rowIDKey})
	}
	return dataQuery(schema, name, rowID, filter, orderBy)
}

func dataQuery(schema, name string, rowID bool, filter string, orderBy []string) (string, error) {
	var b strings.Builder
	if rowID {
		b.WriteString(`SELECT t.*, t.ROWID AS "` + rowIDKey + `" FROM ` + qualified(schema, name) + " t")
	} else {
		b.WriteString("SELECT * FROM " + qualified(schema, name))
	}
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

// sortTerm turns `COL`, `COL ASC`, `COL DESC` or `"my col" DESC` into a
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
	return quoted(col) + dir, nil
}

// sortColumn unquotes a column name; Oracle names cannot contain double
// quotes, so a quoted name is simply stripped.
func sortColumn(t string) (string, bool) {
	if t == "" || strings.ContainsRune(t, 0) {
		return "", false
	}
	if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
		inner := t[1 : len(t)-1]
		if inner == "" || strings.Contains(inner, `"`) {
			return "", false
		}
		return inner, true
	}
	if strings.ContainsAny(t, " \t\r\n\"") {
		return "", false
	}
	return t, true
}

// rowKeyQuery returns a table's primary key columns, the columns of its best
// unique index whose columns are all NOT NULL (plain columns only, fewest
// columns, then by name), and whether the table exists. Lists are separated
// by CHR(31).
const rowKeyQuery = `
SELECT
  (SELECT LISTAGG(cc.column_name, CHR(31)) WITHIN GROUP (ORDER BY cc.position)
   FROM all_constraints k
   JOIN all_cons_columns cc ON cc.owner = k.owner AND cc.constraint_name = k.constraint_name
   WHERE k.owner = :1 AND k.table_name = :2 AND k.constraint_type = 'P'),
  (SELECT u.cols FROM (
     SELECT i.index_name, COUNT(*) AS n,
            LISTAGG(ic.column_name, CHR(31)) WITHIN GROUP (ORDER BY ic.column_position) AS cols,
            MIN(CASE WHEN tc.nullable = 'N' THEN 1 ELSE 0 END) AS notnull
     FROM all_indexes i
     JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
     LEFT JOIN all_tab_cols tc
       ON tc.owner = i.table_owner AND tc.table_name = i.table_name AND tc.column_name = ic.column_name AND tc.hidden_column = 'NO'
     WHERE i.table_owner = :3 AND i.table_name = :4 AND i.uniqueness = 'UNIQUE' AND i.index_type = 'NORMAL'
       AND i.status IN ('VALID', 'N/A')
     GROUP BY i.index_name
   ) u
   WHERE u.notnull = 1
   ORDER BY u.n, u.index_name
   FETCH FIRST 1 ROW ONLY),
  (SELECT COUNT(*) FROM all_tables t WHERE t.owner = :5 AND t.table_name = :6)
FROM dual`

// RowKey returns the primary key columns of a table, or else the columns of
// its first unique, all-NOT-NULL index on plain columns, or else ROWID (the
// data view selects it, see DataQuery). Views and materialized views return
// nil.
func (c *conn) RowKey(ctx context.Context, target *driver.Node) ([]string, error) {
	folder, schema, name, err := relation(target)
	if err != nil {
		return nil, err
	}
	if folder != folderTables {
		return nil, nil
	}
	return c.rowKey(ctx, schema, name)
}

func (c *conn) rowKey(ctx context.Context, schema, name string) ([]string, error) {
	var pk, uniq string
	var exists int64
	err := c.with(ctx, func(l *link) error {
		return l.queryRow(ctx, rowKeyQuery, []any{schema, name, schema, name, schema, name}, &pk, &uniq, &exists)
	})
	switch {
	case err != nil:
		return nil, err
	case exists == 0:
		return nil, fmt.Errorf("table %s.%s does not exist", schema, name)
	case pk != "":
		return strings.Split(pk, "\x1f"), nil
	case uniq != "":
		return strings.Split(uniq, "\x1f"), nil
	}
	return []string{rowIDKey}, nil
}

// colMeta is what Apply needs to know about a column.
type colMeta struct {
	name     string
	typ      string // data_type: NUMBER, DATE, TIMESTAMP(6) WITH TIME ZONE, ...
	identity string // "", ALWAYS, BY DEFAULT
	virtual  bool
	pseudo   bool // ROWID
}

const editColumnsQuery = `
SELECT c.column_name, c.data_type, c.virtual_column,
       (SELECT x.generation_type FROM all_tab_identity_cols x
        WHERE x.owner = c.owner AND x.table_name = c.table_name AND x.column_name = c.column_name)
FROM all_tab_cols c
WHERE c.owner = :1 AND c.table_name = :2 AND c.hidden_column = 'NO'
ORDER BY c.column_id`

// applyNLS makes the server parse values typed by the user as ISO-like
// literals, whatever the database defaults are.
const applyNLS = `ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD HH24:MI:SS'
  NLS_TIMESTAMP_FORMAT = 'YYYY-MM-DD HH24:MI:SS.FF9'
  NLS_TIMESTAMP_TZ_FORMAT = 'YYYY-MM-DD HH24:MI:SS.FF9 TZH:TZM'
  NLS_NUMERIC_CHARACTERS = '.,'`

// Apply saves edits to a table in one transaction, on a dedicated session
// that is closed afterwards (user sessions and their transactions are never
// involved).
//
// Values and keys are strings as typed by the user, nil for NULL, or values
// as decoded by this driver (int64, float64, driver.Number, time.Time,
// []byte, ...). Strings are converted by the server as literals of the
// column's type, with dates as YYYY-MM-DD HH24:MI:SS, timestamps with
// optional .FF9 fractions and TZH:TZM offsets, and "." as decimal point;
// strings for RAW and BLOB columns are hex.
//
//   - OpUpdate sets Values on the row matched by Key; it must match exactly
//     one row.
//   - OpInsert inserts Values; nil values are inserted as explicit NULLs,
//     columns not in Values get their defaults. Virtual and GENERATED
//     ALWAYS identity columns are always omitted, BY DEFAULT identity
//     columns when nil.
//   - OpDelete deletes the row matched by Key; it must match exactly one row.
//
// Key may hold ROWID (see RowKey); a ROWID among Values is ignored. A NULL
// key value matches with IS NULL. If any change fails, or an update or
// delete does not match exactly one row (the row changed or was deleted
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
	l, err := c.st.dial(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	tx, err := l.oc.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed && l.alive() {
			tx.Rollback()
		}
	}()
	if _, err := l.exec(ctx, applyNLS); err != nil {
		return queryError(err, "")
	}
	cols, err := editColumns(ctx, l, schema, name)
	if err != nil {
		return err
	}
	inserts := 0
	for i, ch := range changes {
		if ch.Op == driver.OpInsert {
			inserts++
		}
		st, err := buildChange(qualified(schema, name), cols, ch, inserts)
		if err != nil {
			return fmt.Errorf("change %d: %w", i+1, err)
		}
		if st.sql == "" {
			continue
		}
		n, err := l.exec(ctx, st.sql, st.args...)
		if err != nil {
			l.check(err)
			return changeError(err, st.label)
		}
		if ch.Op != driver.OpInsert {
			switch {
			case n == 0:
				return fmt.Errorf("row %s changed or was deleted by someone else; no changes were saved", st.key)
			case n > 1:
				return fmt.Errorf("row %s is not unique (it matches %d rows); no changes were saved", st.key, n)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return changeError(err, "saving changes")
	}
	committed = true
	return nil
}

func editColumns(ctx context.Context, l *link, schema, name string) ([]colMeta, error) {
	var cols []colMeta
	err := l.query(ctx, editColumnsQuery, []any{schema, name}, func(r []sqldriver.Value) error {
		cols = append(cols, colMeta{name: str(r[0]), typ: str(r[1]), virtual: str(r[2]) == "YES", identity: str(r[3])})
		return nil
	})
	if err != nil {
		return nil, queryError(err, "")
	}
	if cols == nil {
		return nil, fmt.Errorf("table %s.%s does not exist", schema, name)
	}
	return append(cols, colMeta{name: rowIDKey, typ: "ROWID", pseudo: true}), nil
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
	label string // "updating row ID=42"
	key   string // "ID=42"
}

// buildChange generates the statement for ch on table (already quoted).
// cols lists the table's columns in order (plus the ROWID pseudo-column);
// insertNo numbers inserts for error messages. An update without values
// yields an empty statement.
func buildChange(table string, cols []colMeta, ch driver.RowChange, insertNo int) (change, error) {
	var st change
	param := func(v any, col colMeta) (string, error) {
		bind, fn, err := bindValue(v, col.typ)
		if err != nil {
			return "", err
		}
		st.args = append(st.args, bind)
		p := ":" + strconv.Itoa(len(st.args))
		if fn != "" {
			return fmt.Sprintf(fn, p), nil
		}
		return p, nil
	}
	values, err := ordered(cols, ch.Values)
	if err != nil {
		return st, err
	}
	values = slices.DeleteFunc(values, func(c colMeta) bool { return c.pseudo })

	if ch.Op == driver.OpInsert {
		st.label = fmt.Sprintf("inserting new row %d", insertNo)
		var names, params []string
		for _, col := range values {
			v := ch.Values[col.name]
			if col.virtual || col.identity == "ALWAYS" || col.identity != "" && v == nil {
				continue
			}
			p, err := param(v, col)
			if err != nil {
				return st, fmt.Errorf("column %s: %w", col.name, err)
			}
			names = append(names, quoted(col.name))
			params = append(params, p)
		}
		if len(names) == 0 {
			// Oracle has no DEFAULT VALUES; the first ordinary column takes its default.
			i := slices.IndexFunc(cols, func(c colMeta) bool { return !c.virtual && !c.pseudo && c.identity == "" })
			if i < 0 {
				i = slices.IndexFunc(cols, func(c colMeta) bool { return !c.pseudo })
			}
			st.sql = "INSERT INTO " + table + " (" + quoted(cols[i].name) + ") VALUES (DEFAULT)"
			return st, nil
		}
		st.sql = "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(params, ", ") + ")"
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
			p, err := param(ch.Values[col.name], col)
			if err != nil {
				return st, fmt.Errorf("column %s: %w", col.name, err)
			}
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoted(col.name) + " = " + p)
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
		name := quoted(col.name)
		if col.pseudo {
			name = "ROWID"
		}
		b.WriteString(name)
		v := ch.Key[col.name]
		if v == nil {
			b.WriteString(" IS NULL")
			continue
		}
		p, err := param(v, col)
		if err != nil {
			return st, fmt.Errorf("key column %s: %w", col.name, err)
		}
		b.WriteString(" = " + p)
	}
	st.sql = b.String()
	return st, nil
}

// bindValue converts a value for a column of type typ into a bind value and
// an optional SQL wrapper (a format with one %s for the placeholder).
// Driver-typed values get explicit conversions; strings are left to the
// server, which parses them with the session's NLS formats (see applyNLS).
func bindValue(v any, typ string) (any, string, error) {
	switch x := v.(type) {
	case nil:
		return nil, "", nil
	case []byte:
		return x, "", nil
	case string:
		if isBinary(typ) {
			b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(x, "0x"), "\\x"))
			if err != nil {
				return nil, "", fmt.Errorf("invalid hex value %q", x)
			}
			return b, "", nil
		}
		return x, "", nil
	case time.Time:
		switch {
		case typ == "DATE":
			return x.Format("2006-01-02 15:04:05"), "TO_DATE(%s, 'YYYY-MM-DD HH24:MI:SS')", nil
		case strings.Contains(typ, "TIME ZONE"):
			return x.Format("2006-01-02 15:04:05.000000000 -07:00"), "TO_TIMESTAMP_TZ(%s, 'YYYY-MM-DD HH24:MI:SS.FF9 TZH:TZM')", nil
		default:
			return x.Format("2006-01-02 15:04:05.000000000"), "TO_TIMESTAMP(%s, 'YYYY-MM-DD HH24:MI:SS.FF9')", nil
		}
	case bool:
		if typ == "BOOLEAN" {
			return strings.ToUpper(strconv.FormatBool(x)), "", nil
		}
		if x {
			return "1", "", nil
		}
		return "0", "", nil
	}
	s, err := textValue(v)
	return s, "", err
}

func isBinary(typ string) bool {
	return typ == "RAW" || typ == "BLOB" || typ == "LONG RAW"
}

// textValue renders a non-string value as Oracle would parse it.
func textValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case driver.Number:
		return string(x), nil
	case driver.JSON:
		return string(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case int:
		return strconv.Itoa(x), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case float64:
		return formatFloat(x, 64), nil
	case float32:
		return formatFloat(float64(x), 32), nil
	case bool:
		return strings.ToUpper(strconv.FormatBool(x)), nil
	case []byte:
		return hex.EncodeToString(x), nil
	case time.Time:
		return x.Format("2006-01-02 15:04:05.999999999"), nil
	case fmt.Stringer:
		return x.String(), nil
	}
	return "", fmt.Errorf("unsupported value type %T", v)
}

// formatFloat renders floats; BINARY_FLOAT/DOUBLE accept Inf and Nan.
func formatFloat(f float64, bits int) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, bits)
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

// keyDesc renders a row key for messages: ID=42, CODE='x', PARENT=NULL.
func keyDesc(keys []colMeta, m map[string]any) string {
	parts := make([]string, len(keys))
	for i, col := range keys {
		var s string
		switch x := m[col.name].(type) {
		case nil:
			s = "NULL"
		case string:
			s = x
			if x == "" || strings.ContainsAny(x, " \t\r\n,'=") {
				s = literal(x)
			}
		default:
			t, err := textValue(x)
			if err != nil {
				t = fmt.Sprint(x)
			}
			s = t
		}
		parts[i] = col.name + "=" + s
	}
	return strings.Join(parts, ", ")
}

// literal quotes s as a SQL string literal.
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
