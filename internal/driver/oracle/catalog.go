package oracle

import (
	"context"
	sqldriver "database/sql/driver"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// Explorer tree paths: [schema, folderKey, object, subFolderKey, item].
// Roots are schemas (users owning objects, the current user first);
// Oracle-maintained schemas are hidden unless the connection option
// show_system_schemas is true. Packages and triggers are NodeFunction nodes
// and synonyms NodeView nodes, told apart by their Detail.
const (
	folderTables      = "tables"
	folderViews       = "views"
	folderMatviews    = "matviews"
	folderSequences   = "sequences"
	folderProcedures  = "procedures"
	folderFunctions   = "functions"
	folderPackages    = "packages"
	folderTriggers    = "triggers"
	folderSynonyms    = "synonyms"
	folderColumns     = "columns"
	folderIndexes     = "indexes"
	folderConstraints = "constraints"
)

func (c *conn) Children(ctx context.Context, parent *driver.Node) ([]*driver.Node, error) {
	if parent == nil {
		return c.schemas(ctx)
	}
	p := parent.Path
	switch len(p) {
	case 1:
		return c.folders(ctx, p)
	case 2:
		return c.objects(ctx, p)
	case 3:
		return c.subFolders(ctx, p)
	case 4:
		return c.items(ctx, p)
	}
	return nil, nil
}

func child(parent []string, key string, kind driver.NodeKind, name, detail string, leaf bool) *driver.Node {
	path := append(parent[:len(parent):len(parent)], key)
	return &driver.Node{Path: path, Kind: kind, Name: name, Detail: detail, Leaf: leaf}
}

// list runs a catalog query on the metadata session, calling scan per row.
func (c *conn) list(ctx context.Context, sql string, args []any, scan func(row []sqldriver.Value) error) error {
	return c.with(ctx, func(l *link) error { return l.query(ctx, sql, args, scan) })
}

const schemasQuery = `
SELECT u.username, u.oracle_maintained
FROM all_users u
WHERE u.username = USER
   OR ((u.oracle_maintained = 'N' OR :1 = 'Y') AND EXISTS (SELECT 1 FROM all_objects o WHERE o.owner = u.username))
ORDER BY CASE WHEN u.username = USER THEN 0 ELSE 1 END, u.oracle_maintained, u.username`

func (c *conn) schemas(ctx context.Context) ([]*driver.Node, error) {
	show := "N"
	if c.showSystem {
		show = "Y"
	}
	var out []*driver.Node
	err := c.list(ctx, schemasQuery, []any{show}, func(r []sqldriver.Value) error {
		detail := ""
		if str(r[1]) == "Y" {
			detail = "system"
		}
		out = append(out, child(nil, str(r[0]), driver.NodeSchema, str(r[0]), detail, false))
		return nil
	})
	return out, err
}

// tableFilter keeps the tables a user thinks of as tables: no nested,
// secondary (domain index), overflow, dropped or materialized view container
// tables.
const tableFilter = `t.nested = 'NO' AND t.secondary = 'N' AND t.dropped = 'NO'
  AND (t.iot_type IS NULL OR t.iot_type = 'IOT')
  AND NOT EXISTS (SELECT 1 FROM all_mviews m WHERE m.owner = t.owner AND m.mview_name = t.table_name)`

const foldersQuery = `
SELECT (SELECT COUNT(*) FROM all_tables t WHERE t.owner = :1 AND ` + tableFilter + `),
       (SELECT COUNT(*) FROM all_views WHERE owner = :2),
       (SELECT COUNT(*) FROM all_mviews WHERE owner = :3),
       (SELECT COUNT(*) FROM all_sequences WHERE sequence_owner = :4 AND sequence_name NOT LIKE 'ISEQ$$%'),
       (SELECT COUNT(*) FROM all_objects WHERE owner = :5 AND object_type = 'PROCEDURE'),
       (SELECT COUNT(*) FROM all_objects WHERE owner = :6 AND object_type = 'FUNCTION'),
       (SELECT COUNT(*) FROM all_objects WHERE owner = :7 AND object_type = 'PACKAGE'),
       (SELECT COUNT(*) FROM all_triggers WHERE owner = :8),
       (SELECT COUNT(*) FROM all_synonyms WHERE owner = :9)
FROM dual`

var folderNames = []struct{ key, name string }{
	{folderTables, "Tables"},
	{folderViews, "Views"},
	{folderMatviews, "Materialized views"},
	{folderSequences, "Sequences"},
	{folderProcedures, "Procedures"},
	{folderFunctions, "Functions"},
	{folderPackages, "Packages"},
	{folderTriggers, "Triggers"},
	{folderSynonyms, "Synonyms"},
}

// repeat returns v n times, for queries binding the same value positionally.
func repeat(v any, n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func (c *conn) folders(ctx context.Context, p []string) ([]*driver.Node, error) {
	var out []*driver.Node
	err := c.list(ctx, foldersQuery, repeat(p[0], len(folderNames)), func(r []sqldriver.Value) error {
		for i, f := range folderNames {
			n := integer(r[i])
			if n > 0 || f.key == folderTables {
				out = append(out, child(p, f.key, driver.NodeFolder, f.name, fmt.Sprintf("(%d)", n), false))
			}
		}
		return nil
	})
	return out, err
}

const (
	tablesQuery = `
SELECT t.table_name, t.num_rows, t.temporary,
       CASE WHEN EXISTS (SELECT 1 FROM all_external_tables e WHERE e.owner = t.owner AND e.table_name = t.table_name) THEN 'Y' ELSE 'N' END
FROM all_tables t
WHERE t.owner = :1 AND ` + tableFilter + `
ORDER BY t.table_name`

	viewsQuery = `
SELECT object_name, status FROM all_objects WHERE owner = :1 AND object_type = 'VIEW' ORDER BY object_name`

	matviewsQuery = `
SELECT m.mview_name, o.status
FROM all_mviews m
LEFT JOIN all_objects o ON o.owner = m.owner AND o.object_name = m.mview_name AND o.object_type = 'MATERIALIZED VIEW'
WHERE m.owner = :1
ORDER BY m.mview_name`

	sequencesQuery = `
SELECT sequence_name FROM all_sequences WHERE sequence_owner = :1 AND sequence_name NOT LIKE 'ISEQ$$%' ORDER BY sequence_name`

	// routinesQuery lists standalone procedures or functions with their
	// arguments (position 0 is a function's return type).
	routinesQuery = `
SELECT o.object_name, o.status, a.args, a.ret
FROM all_objects o
LEFT JOIN (
	SELECT object_name,
	       LISTAGG(CASE WHEN position > 0 AND argument_name IS NOT NULL THEN
	                 argument_name || CASE in_out WHEN 'IN' THEN '' ELSE ' ' || REPLACE(in_out, '/', ' ') END || ' ' || data_type
	               END, ', ') WITHIN GROUP (ORDER BY position) AS args,
	       MAX(CASE WHEN position = 0 THEN data_type END) AS ret
	FROM all_arguments
	WHERE owner = :1 AND package_name IS NULL AND data_level = 0
	GROUP BY object_name
) a ON a.object_name = o.object_name
WHERE o.owner = :2 AND o.object_type = :3
ORDER BY o.object_name`

	packagesQuery = `
SELECT object_name, MIN(status) FROM all_objects
WHERE owner = :1 AND object_type IN ('PACKAGE', 'PACKAGE BODY')
GROUP BY object_name
HAVING COUNT(CASE WHEN object_type = 'PACKAGE' THEN 1 END) > 0
ORDER BY object_name`

	triggersQuery = `
SELECT t.trigger_name, t.table_name, t.status, o.status
FROM all_triggers t
LEFT JOIN all_objects o ON o.owner = t.owner AND o.object_name = t.trigger_name AND o.object_type = 'TRIGGER'
WHERE t.owner = :1
ORDER BY t.trigger_name`

	synonymsQuery = `
SELECT synonym_name, table_owner, table_name, db_link FROM all_synonyms WHERE owner = :1 ORDER BY synonym_name`
)

func (c *conn) objects(ctx context.Context, p []string) ([]*driver.Node, error) {
	schema := p[0]
	var out []*driver.Node
	add := func(name string, kind driver.NodeKind, detail string, leaf bool) {
		out = append(out, child(p, name, kind, name, detail, leaf))
	}
	var err error
	switch p[1] {
	case folderTables:
		err = c.list(ctx, tablesQuery, []any{schema}, func(r []sqldriver.Value) error {
			detail := rowEstimate(r[1])
			switch {
			case str(r[2]) == "Y":
				detail = "temporary"
			case str(r[3]) == "Y":
				detail = "external"
			}
			add(str(r[0]), driver.NodeTable, detail, false)
			return nil
		})
	case folderViews:
		err = c.list(ctx, viewsQuery, []any{schema}, func(r []sqldriver.Value) error {
			add(str(r[0]), driver.NodeView, invalid(r[1], ""), false)
			return nil
		})
	case folderMatviews:
		err = c.list(ctx, matviewsQuery, []any{schema}, func(r []sqldriver.Value) error {
			add(str(r[0]), driver.NodeView, invalid(r[1], "materialized"), false)
			return nil
		})
	case folderSequences:
		err = c.list(ctx, sequencesQuery, []any{schema}, func(r []sqldriver.Value) error {
			add(str(r[0]), driver.NodeSequence, "", true)
			return nil
		})
	case folderProcedures, folderFunctions:
		typ := map[string]string{folderProcedures: "PROCEDURE", folderFunctions: "FUNCTION"}[p[1]]
		err = c.list(ctx, routinesQuery, []any{schema, schema, typ}, func(r []sqldriver.Value) error {
			add(str(r[0]), driver.NodeFunction, invalid(r[1], signature(r[2], r[3])), true)
			return nil
		})
	case folderPackages:
		err = c.list(ctx, packagesQuery, []any{schema}, func(r []sqldriver.Value) error {
			add(str(r[0]), driver.NodeFunction, invalid(r[1], "package"), true)
			return nil
		})
	case folderTriggers:
		err = c.list(ctx, triggersQuery, []any{schema}, func(r []sqldriver.Value) error {
			detail := "trigger"
			if t := str(r[1]); t != "" {
				detail = "on " + t
			}
			if str(r[2]) == "DISABLED" {
				detail += " disabled"
			}
			add(str(r[0]), driver.NodeFunction, invalid(r[3], detail), true)
			return nil
		})
	case folderSynonyms:
		err = c.list(ctx, synonymsQuery, []any{schema}, func(r []sqldriver.Value) error {
			target := str(r[2])
			if o := str(r[1]); o != "" {
				target = o + "." + target
			}
			if l := str(r[3]); l != "" {
				target += "@" + l
			}
			add(str(r[0]), driver.NodeView, "→ "+target, true)
			return nil
		})
	default:
		return nil, nil
	}
	return out, err
}

// invalid appends "invalid" to detail for objects whose status says so.
func invalid(status any, detail string) string {
	if str(status) != "INVALID" {
		return detail
	}
	return strings.TrimSpace(detail + " invalid")
}

// signature renders "(A NUMBER, B OUT VARCHAR2) → NUMBER".
func signature(args, ret any) string {
	s := "(" + str(args) + ")"
	if r := str(ret); r != "" {
		s += " → " + r
	}
	return s
}

const subFoldersQuery = `
SELECT (SELECT COUNT(*) FROM all_tab_columns WHERE owner = :1 AND table_name = :2),
       (SELECT COUNT(*) FROM all_indexes WHERE table_owner = :3 AND table_name = :4 AND index_type <> 'LOB'),
       (SELECT COUNT(*) FROM all_constraints c WHERE c.owner = :5 AND c.table_name = :6 AND ` + userConstraint + `)
FROM dual`

// userConstraint keeps primary, unique, foreign key and check constraints,
// except the system-named NOT NULL checks every NOT NULL column has.
const userConstraint = `c.constraint_type IN ('P', 'U', 'R', 'C')
  AND NOT (c.constraint_type = 'C' AND c.generated = 'GENERATED NAME' AND c.search_condition_vc LIKE '"%" IS NOT NULL')`

func (c *conn) subFolders(ctx context.Context, p []string) ([]*driver.Node, error) {
	switch p[1] {
	case folderTables, folderViews, folderMatviews:
	default:
		return nil, nil
	}
	args := []any{p[0], p[2], p[0], p[2], p[0], p[2]}
	var out []*driver.Node
	err := c.list(ctx, subFoldersQuery, args, func(r []sqldriver.Value) error {
		out = append(out, child(p, folderColumns, driver.NodeFolder, "Columns", fmt.Sprintf("(%d)", integer(r[0])), false))
		if p[1] != folderViews {
			out = append(out, child(p, folderIndexes, driver.NodeFolder, "Indexes", fmt.Sprintf("(%d)", integer(r[1])), false))
		}
		if p[1] == folderTables {
			out = append(out, child(p, folderConstraints, driver.NodeFolder, "Constraints", fmt.Sprintf("(%d)", integer(r[2])), false))
		}
		return nil
	})
	return out, err
}

func (c *conn) items(ctx context.Context, p []string) ([]*driver.Node, error) {
	schema, table := p[0], p[2]
	var out []*driver.Node
	err := c.with(ctx, func(l *link) error {
		switch p[3] {
		case folderColumns:
			cols, err := columnDefs(ctx, l, schema, table)
			for _, col := range cols {
				out = append(out, child(p, col.name, driver.NodeColumn, col.name, col.detail(), true))
			}
			return err
		case folderIndexes:
			idx, err := indexDefs(ctx, l, schema, table)
			for _, x := range idx {
				out = append(out, child(p, x.name, driver.NodeIndex, x.name, x.detail(), true))
			}
			return err
		case folderConstraints:
			cons, err := constraintDefs(ctx, l, schema, table)
			for _, k := range cons {
				out = append(out, child(p, k.name, driver.NodeIndex, k.name, k.def, true))
			}
			return err
		}
		return nil
	})
	return out, err
}

// rowEstimate renders all_tables.num_rows like "~12k"; NULL (never
// analyzed) is "".
func rowEstimate(v any) string {
	if v == nil {
		return ""
	}
	n := float(v)
	if n < 0 || math.IsNaN(n) {
		return ""
	}
	units := []struct {
		div    float64
		suffix string
	}{{1e9, "B"}, {1e6, "M"}, {1e3, "k"}}
	for _, u := range units {
		if n >= u.div {
			v := n / u.div
			if v < 10 {
				return fmt.Sprintf("~%.1f%s", math.Floor(v*10)/10, u.suffix)
			}
			return fmt.Sprintf("~%.0f%s", math.Floor(v), u.suffix)
		}
	}
	return fmt.Sprintf("~%.0f", n)
}

// colDef is a table column as the explorer and Describe show it.
type colDef struct {
	name, typ, def, comment string
	identity                string // "", "ALWAYS", "BY DEFAULT", "BY DEFAULT ON NULL"
	virtual, notNull, pk    bool
}

// Column details come in two simple queries: joining the dictionary views
// (even pre-filtered) makes the optimizer fall back to per-row lookups that
// take half a second.
const (
	columnsQuery = `
SELECT column_name, data_type, data_length, data_precision, data_scale, char_length, char_used,
       data_type_owner, nullable, virtual_column, default_on_null, data_default
FROM all_tab_cols
WHERE owner = :1 AND table_name = :2 AND hidden_column = 'NO'
ORDER BY column_id`

	// columnExtrasQuery returns identity generation types (I), comments (C)
	// and primary key columns (P).
	columnExtrasQuery = `
SELECT 'I', column_name, generation_type FROM all_tab_identity_cols WHERE owner = :1 AND table_name = :2
UNION ALL
SELECT 'C', column_name, comments FROM all_col_comments WHERE owner = :3 AND table_name = :4 AND comments IS NOT NULL
UNION ALL
SELECT 'P', cc.column_name, NULL
FROM all_constraints k
JOIN all_cons_columns cc ON cc.owner = k.owner AND cc.constraint_name = k.constraint_name
WHERE k.owner = :5 AND k.table_name = :6 AND k.constraint_type = 'P'`
)

func columnDefs(ctx context.Context, l *link, schema, table string) ([]colDef, error) {
	var out []colDef
	err := l.query(ctx, columnsQuery, []any{schema, table}, func(r []sqldriver.Value) error {
		c := colDef{
			name:    str(r[0]),
			typ:     typeOf(r[1:8]),
			notNull: str(r[8]) == "N",
			virtual: str(r[9]) == "YES",
			def:     strings.TrimSpace(str(r[11])),
		}
		if str(r[10]) == "YES" {
			c.identity = " ON NULL" // completed by the identity row below
		}
		out = append(out, c)
		return nil
	})
	if err != nil || len(out) == 0 {
		return out, err
	}
	err = l.query(ctx, columnExtrasQuery, repeatPair(schema, table, 3), func(r []sqldriver.Value) error {
		i := slices.IndexFunc(out, func(c colDef) bool { return c.name == str(r[1]) })
		if i < 0 {
			return nil
		}
		switch c := &out[i]; str(r[0]) {
		case "I":
			c.identity = str(r[2]) + c.identity
		case "C":
			c.comment = str(r[2])
		case "P":
			c.pk = true
		}
		return nil
	})
	for i := range out {
		if strings.HasPrefix(out[i].identity, " ") {
			out[i].identity = "" // DEFAULT ON NULL without identity
		}
	}
	return out, err
}

// repeatPair returns a, b repeated n times, for queries binding the same
// values positionally.
func repeatPair(a, b any, n int) []any {
	out := make([]any, 0, 2*n)
	for range n {
		out = append(out, a, b)
	}
	return out
}

// defaultClause renders the column's identity, virtual or default clause
// as DDL would, or "".
func (c colDef) defaultClause() string {
	switch {
	case c.identity != "":
		return "GENERATED " + c.identity + " AS IDENTITY"
	case c.virtual:
		return "GENERATED ALWAYS AS (" + c.def + ") VIRTUAL"
	}
	return c.def
}

func (c colDef) detail() string {
	s := c.typ
	if c.notNull && c.identity == "" {
		s += " not null"
	}
	switch {
	case c.identity != "":
		s += " identity"
	case c.virtual:
		s += " virtual"
	case c.def != "" && !strings.EqualFold(c.def, "NULL"):
		s += " = " + shortDefault(c.def)
	}
	return s
}

func shortDefault(def string) string {
	const maxLen = 32
	if r := []rune(def); len(r) > maxLen {
		return string(r[:maxLen-1]) + "…"
	}
	return def
}

type idxDef struct {
	owner, name, typ string
	unique, pk       bool
	cols             []string
}

// indexesQuery lists index columns in order; descending and function-based
// keys come from their expression (a LONG, so it is fetched as is).
const indexesQuery = `
SELECT i.owner, i.index_name, i.uniqueness, i.index_type, c.column_name, c.descend, e.column_expression,
       (SELECT COUNT(*) FROM all_constraints k
        WHERE k.owner = i.table_owner AND k.table_name = i.table_name AND k.index_name = i.index_name AND k.constraint_type = 'P')
FROM all_indexes i
JOIN all_ind_columns c ON c.index_owner = i.owner AND c.index_name = i.index_name
LEFT JOIN all_ind_expressions e
  ON e.index_owner = c.index_owner AND e.index_name = c.index_name AND e.column_position = c.column_position
WHERE i.table_owner = :1 AND i.table_name = :2 AND i.index_type <> 'LOB'
ORDER BY i.index_name, c.column_position`

func indexDefs(ctx context.Context, l *link, schema, table string) ([]idxDef, error) {
	var out []idxDef
	err := l.query(ctx, indexesQuery, []any{schema, table}, func(r []sqldriver.Value) error {
		name := str(r[1])
		if n := len(out); n == 0 || out[n-1].name != name || out[n-1].owner != str(r[0]) {
			out = append(out, idxDef{owner: str(r[0]), name: name, typ: str(r[3]), unique: str(r[2]) == "UNIQUE", pk: integer(r[7]) > 0})
		}
		col := quoteIdent(str(r[4]))
		if e := strings.TrimSpace(str(r[6])); e != "" {
			col = e
		}
		if str(r[5]) == "DESC" {
			col += " DESC"
		}
		x := &out[len(out)-1]
		x.cols = append(x.cols, col)
		return nil
	})
	slices.SortStableFunc(out, func(a, b idxDef) int {
		if a.pk != b.pk {
			if a.pk {
				return -1
			}
			return 1
		}
		return 0
	})
	return out, err
}

// detail turns an index into "(A, UPPER("B") DESC) unique".
func (x idxDef) detail() string {
	s := "(" + strings.Join(x.cols, ", ") + ")"
	switch {
	case strings.Contains(x.typ, "BITMAP"):
		s = "bitmap " + s
	case strings.HasPrefix(x.typ, "DOMAIN"):
		s = "domain " + s
	}
	switch {
	case x.pk:
		s += " primary"
	case x.unique:
		s += " unique"
	}
	return s
}

// definition renders the index as a CREATE INDEX statement.
func (x idxDef) definition(schema, table string) string {
	kind := ""
	switch {
	case x.unique:
		kind = "UNIQUE "
	case strings.Contains(x.typ, "BITMAP"):
		kind = "BITMAP "
	}
	return fmt.Sprintf("CREATE %sINDEX %s.%s ON %s.%s (%s)", kind, quoteIdent(x.owner), quoteIdent(x.name),
		quoteIdent(schema), quoteIdent(table), strings.Join(x.cols, ", "))
}

type conDef struct{ name, typ, def string }

const constraintsQuery = `
SELECT c.constraint_name, c.constraint_type, c.search_condition_vc, c.delete_rule, c.status,
       (SELECT LISTAGG(cc.column_name, CHR(31)) WITHIN GROUP (ORDER BY cc.position)
        FROM all_cons_columns cc WHERE cc.owner = c.owner AND cc.constraint_name = c.constraint_name),
       r.owner, r.table_name,
       (SELECT LISTAGG(rc.column_name, CHR(31)) WITHIN GROUP (ORDER BY rc.position)
        FROM all_cons_columns rc WHERE rc.owner = r.owner AND rc.constraint_name = r.constraint_name)
FROM all_constraints c
LEFT JOIN all_constraints r ON r.owner = c.r_owner AND r.constraint_name = c.r_constraint_name
WHERE c.owner = :1 AND c.table_name = :2 AND ` + userConstraint + `
ORDER BY DECODE(c.constraint_type, 'P', 1, 'U', 2, 'R', 3, 4), c.constraint_name`

func constraintDefs(ctx context.Context, l *link, schema, table string) ([]conDef, error) {
	var out []conDef
	err := l.query(ctx, constraintsQuery, []any{schema, table}, func(r []sqldriver.Value) error {
		k := conDef{name: str(r[0]), typ: str(r[1])}
		cols := identList(str(r[5]))
		switch k.typ {
		case "P":
			k.def = "PRIMARY KEY (" + cols + ")"
		case "U":
			k.def = "UNIQUE (" + cols + ")"
		case "R":
			k.def = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s.%s (%s)", cols, quoteIdent(str(r[6])), quoteIdent(str(r[7])), identList(str(r[8])))
			if rule := str(r[3]); rule != "" && rule != "NO ACTION" {
				k.def += " ON DELETE " + rule
			}
		case "C":
			k.def = "CHECK (" + strings.TrimSpace(str(r[2])) + ")"
		}
		if str(r[4]) == "DISABLED" {
			k.def += " DISABLE"
		}
		out = append(out, k)
		return nil
	})
	return out, err
}

// identList quotes a CHR(31)-separated list of names.
func identList(s string) string {
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "\x1f")
	for i, p := range parts {
		parts[i] = quoteIdent(p)
	}
	return strings.Join(parts, ", ")
}

// reserved are Oracle's reserved words (V$RESERVED_WORDS where RESERVED = 'Y'),
// which must be quoted as identifiers.
var reserved = strings.Fields(`ACCESS ADD ALL ALTER AND ANY AS ASC AUDIT BETWEEN BY CHAR CHECK CLUSTER COLUMN
	COMMENT COMPRESS CONNECT CREATE CURRENT DATE DECIMAL DEFAULT DELETE DESC DISTINCT DROP ELSE EXCLUSIVE
	EXISTS FILE FLOAT FOR FROM GRANT GROUP HAVING IDENTIFIED IMMEDIATE IN INCREMENT INDEX INITIAL INSERT
	INTEGER INTERSECT INTO IS LEVEL LIKE LOCK LONG MAXEXTENTS MINUS MLSLABEL MODE MODIFY NOAUDIT NOCOMPRESS
	NOT NOWAIT NULL NUMBER OF OFFLINE ON ONLINE OPTION OR ORDER PCTFREE PRIOR PUBLIC RAW RENAME RESOURCE
	REVOKE ROW ROWID ROWNUM ROWS SELECT SESSION SET SHARE SIZE SMALLINT START SUCCESSFUL SYNONYM SYSDATE
	TABLE THEN TO TRIGGER UID UNION UNIQUE UPDATE USER VALIDATE VALUES VARCHAR VARCHAR2 VIEW WHENEVER WHERE WITH`)

// quoteIdent quotes a name for display, only when Oracle needs it: names
// that are not upper-case plain identifiers, or are reserved words.
func quoteIdent(name string) string {
	plain := name != "" && name[0] >= 'A' && name[0] <= 'Z'
	for i := 0; plain && i < len(name); i++ {
		c := name[i]
		plain = c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c == '#'
	}
	if plain && !slices.Contains(reserved, name) {
		return name
	}
	return quoted(name)
}

// quoted always quotes a name; Oracle identifiers cannot contain double
// quotes, so none are escaped.
func quoted(name string) string { return `"` + name + `"` }

// qualified quotes schema.name for generated SQL.
func qualified(schema, name string) string {
	return quoted(schema) + "." + quoted(name)
}
