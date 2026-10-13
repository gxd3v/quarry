package postgres

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Describer = (*conn)(nil)

// Describe returns columns, indexes, constraints, triggers, statistics and a
// DDL script for a table (plain, partitioned or foreign), view or
// materialized view. All catalog queries go out in one pipelined round trip.
func (c *conn) Describe(ctx context.Context, target *driver.Node) (*driver.ObjectDetails, error) {
	_, schema, name, err := relation(target)
	if err != nil {
		return nil, err
	}
	var d *driver.ObjectDetails
	err = c.with(ctx, target.Path[0], func(pc *pgx.Conn) error {
		var err error
		d, err = describeRel(ctx, pc, schema, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// batcher is satisfied by *pgx.Conn and pgx.Tx.
type batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// relOID resolves $1.$2 to a pg_class oid.
const relOID = `(SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2)`

// Names prefixed with q are quoted with quote_ident server-side, which knows
// the keyword list.
const describeRelQuery = `
SELECT c.relkind::text, quote_ident(n.nspname) || '.' || quote_ident(c.relname), c.relpersistence::text,
       coalesce(c.reloptions, '{}'), c.relispopulated, coalesce(obj_description(c.oid, 'pg_class'), ''),
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) ELSE '' END,
       coalesce((SELECT quote_ident(pn.nspname) || '.' || quote_ident(p.relname)
                 FROM pg_inherits i
                 JOIN pg_class p ON p.oid = i.inhparent
                 JOIN pg_namespace pn ON pn.oid = p.relnamespace
                 WHERE i.inhrelid = c.oid AND c.relispartition), ''),
       CASE WHEN c.relispartition THEN coalesce(pg_get_expr(c.relpartbound, c.oid), '') ELSE '' END,
       coalesce((SELECT array_agg(quote_ident(pn.nspname) || '.' || quote_ident(p.relname) ORDER BY i.inhseqno)
                 FROM pg_inherits i
                 JOIN pg_class p ON p.oid = i.inhparent
                 JOIN pg_namespace pn ON pn.oid = p.relnamespace
                 WHERE i.inhrelid = c.oid AND NOT c.relispartition), '{}'),
       CASE WHEN c.relkind IN ('v', 'm') THEN coalesce(pg_get_viewdef(c.oid, true), '') ELSE '' END,
       coalesce((SELECT quote_ident(s.srvname) FROM pg_foreign_table f JOIN pg_foreign_server s ON s.oid = f.ftserver
                 WHERE f.ftrelid = c.oid), ''),
       coalesce((SELECT f.ftoptions FROM pg_foreign_table f WHERE f.ftrelid = c.oid), '{}'),
       coalesce((SELECT quote_ident(am.amname) FROM pg_am am WHERE am.oid = c.relam AND am.amname <> 'heap'), ''),
       coalesce((SELECT quote_ident(t.spcname) FROM pg_tablespace t WHERE t.oid = c.reltablespace), ''),
       c.relrowsecurity, c.relforcerowsecurity,
       s.est_rows, s.total, s.tbl, s.idx,
       st.last_vacuum, st.last_autovacuum, st.last_analyze, st.last_autoanalyze, st.seq_scan, st.idx_scan
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN LATERAL (
	-- A partitioned table has no storage of its own: add up its leaves.
	SELECT CASE WHEN bool_and(p.reltuples < 0) THEN -1 ELSE sum(greatest(p.reltuples, 0)) END::float8 AS est_rows,
	       pg_size_pretty(sum(pg_total_relation_size(p.oid))) AS total,
	       pg_size_pretty(sum(pg_table_size(p.oid))) AS tbl,
	       pg_size_pretty(sum(pg_indexes_size(p.oid))) AS idx
	FROM pg_class p
	WHERE (c.relkind <> 'p' AND p.oid = c.oid)
	   OR (c.relkind = 'p' AND p.oid IN (SELECT t.relid FROM pg_partition_tree(c.oid) t WHERE t.isleaf))
) s ON c.relkind IN ('r', 'p', 'm')
LEFT JOIN pg_stat_all_tables st ON st.relid = c.oid
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p', 'f', 'v', 'm')`

const describeColumnsQuery = `
SELECT a.attname::text, quote_ident(a.attname), format_type(a.atttypid, a.atttypmod), a.attnotnull,
       coalesce(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity::text, a.attgenerated::text,
       coalesce(col_description(a.attrelid, a.attnum), ''), a.attislocal,
       coalesce((SELECT quote_ident(cn.nspname) || '.' || quote_ident(co.collname)
                 FROM pg_collation co JOIN pg_namespace cn ON cn.oid = co.collnamespace
                 WHERE co.oid = a.attcollation AND a.attcollation <> t.typcollation), ''),
       coalesce(a.attnum = ANY ((SELECT k.conkey FROM pg_constraint k WHERE k.conrelid = a.attrelid AND k.contype = 'p')::int2[]), false)
FROM pg_attribute a
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = ` + relOID + ` AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

const describeIndexesQuery = `
SELECT i.relname::text, quote_ident(n.nspname) || '.' || quote_ident(i.relname), pg_get_indexdef(x.indexrelid),
       EXISTS (SELECT FROM pg_constraint k
               WHERE k.conindid = x.indexrelid AND k.conrelid = x.indrelid AND k.contype IN ('p', 'u', 'x')),
       EXISTS (SELECT FROM pg_inherits h WHERE h.inhrelid = x.indexrelid),
       coalesce(obj_description(x.indexrelid, 'pg_class'), '')
FROM pg_index x
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_namespace n ON n.oid = i.relnamespace
WHERE x.indrelid = ` + relOID + `
ORDER BY x.indisprimary DESC, i.relname`

const describeConstraintsQuery = `
SELECT k.conname::text, quote_ident(k.conname), k.contype::text, pg_get_constraintdef(k.oid), k.conislocal,
       coalesce(obj_description(k.oid, 'pg_constraint'), '')
FROM pg_constraint k
WHERE k.conrelid = ` + relOID + ` AND k.contype NOT IN ('n', 't')
ORDER BY array_position(ARRAY['p', 'u', 'f', 'c', 'x']::"char"[], k.contype), k.conname`

const describeTriggersQuery = `
SELECT t.tgname::text, quote_ident(t.tgname), pg_get_triggerdef(t.oid, true), t.tgparentid <> 0, t.tgenabled::text,
       coalesce(obj_description(t.oid, 'pg_trigger'), '')
FROM pg_trigger t
WHERE t.tgrelid = ` + relOID + ` AND NOT t.tgisinternal
ORDER BY t.tgname`

// relDef is everything the DDL generator needs about a relation. Fields
// starting with q hold quoted SQL names.
type relDef struct {
	kind        string // r p f v m
	qname       string
	persistence string // p u t
	options     []string
	populated   bool
	comment     string
	partKey     string // "RANGE (logdate)"
	partOf      string
	partBound   string // "FOR VALUES FROM (...) TO (...)" or "DEFAULT"
	inherits    []string
	viewDef     string
	server      string
	ftOptions   []string
	am          string
	tablespace  string
	rls, forced bool

	cols []colDef
	idx  []idxDef
	cons []conDef
	trg  []trgDef

	stats relStats
}

type colDef struct {
	name, qname, typ, def, identity, generated, comment, collation string
	notNull, local, pk                                             bool
}

type idxDef struct {
	name, qname, def, comment string
	constraint, inherited     bool
}

type conDef struct {
	name, qname, typ, def, comment string
	local                          bool
}

type trgDef struct {
	name, qname, def, enabled, comment string
	inherited                          bool
}

type relStats struct {
	rows                                *float64
	total, table, index                 *string
	vacuum, autovacuum, analyze, autoan *time.Time
	seqScan, idxScan                    *int64
}

func describeRel(ctx context.Context, pc batcher, schema, name string) (*driver.ObjectDetails, error) {
	b := &pgx.Batch{}
	for _, q := range []string{describeRelQuery, describeColumnsQuery, describeIndexesQuery, describeConstraintsQuery, describeTriggersQuery} {
		b.Queue(q, schema, name)
	}
	br := pc.SendBatch(ctx, b)
	r, err := scanRelDef(br)
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("%s.%s does not exist", schema, name)
	}
	return r.details(), nil
}

func scanRelDef(br pgx.BatchResults) (*relDef, error) {
	r := &relDef{}
	found := false
	err := scanAll(br, func(rows pgx.Rows) error {
		found = true
		s := &r.stats
		return rows.Scan(&r.kind, &r.qname, &r.persistence, &r.options, &r.populated, &r.comment, &r.partKey,
			&r.partOf, &r.partBound, &r.inherits, &r.viewDef, &r.server, &r.ftOptions, &r.am, &r.tablespace,
			&r.rls, &r.forced, &s.rows, &s.total, &s.table, &s.index,
			&s.vacuum, &s.autovacuum, &s.analyze, &s.autoan, &s.seqScan, &s.idxScan)
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(br, func(rows pgx.Rows) error {
		var c colDef
		err := rows.Scan(&c.name, &c.qname, &c.typ, &c.notNull, &c.def, &c.identity, &c.generated, &c.comment,
			&c.local, &c.collation, &c.pk)
		r.cols = append(r.cols, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(br, func(rows pgx.Rows) error {
		var x idxDef
		err := rows.Scan(&x.name, &x.qname, &x.def, &x.constraint, &x.inherited, &x.comment)
		r.idx = append(r.idx, x)
		return err
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(br, func(rows pgx.Rows) error {
		var k conDef
		err := rows.Scan(&k.name, &k.qname, &k.typ, &k.def, &k.local, &k.comment)
		r.cons = append(r.cons, k)
		return err
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(br, func(rows pgx.Rows) error {
		var t trgDef
		err := rows.Scan(&t.name, &t.qname, &t.def, &t.inherited, &t.enabled, &t.comment)
		r.trg = append(r.trg, t)
		return err
	})
	if err != nil || !found {
		return nil, err
	}
	return r, nil
}

func scanAll(br pgx.BatchResults, scan func(pgx.Rows) error) error {
	rows, err := br.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *relDef) details() *driver.ObjectDetails {
	d := &driver.ObjectDetails{DDL: r.ddl()}
	for _, c := range r.cols {
		d.Columns = append(d.Columns, driver.ColumnInfo{
			Name:       c.name,
			Type:       c.typ,
			Default:    strings.TrimPrefix(strings.TrimSpace(c.defaultClause()), "DEFAULT "),
			Comment:    c.comment,
			Nullable:   !c.notNull,
			PrimaryKey: c.pk,
		})
	}
	for _, x := range r.idx {
		d.Indexes = append(d.Indexes, driver.DetailRow{Name: x.name, Definition: x.def})
	}
	for _, k := range r.cons {
		d.Constraints = append(d.Constraints, driver.DetailRow{Name: k.name, Definition: k.def})
	}
	for _, t := range r.trg {
		d.Triggers = append(d.Triggers, driver.DetailRow{Name: t.name, Definition: t.def})
	}
	d.Stats = r.stats.statRows(r.kind)
	return d
}

// rows2 renders the statistics shown for tables and materialized views.
func (s relStats) statRows(kind string) []driver.DetailRow {
	if kind != "r" && kind != "p" && kind != "m" {
		return nil
	}
	str := func(p *string) string {
		if p == nil {
			return "n/a"
		}
		return *p
	}
	when := func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return t.Local().Format("2006-01-02 15:04:05")
	}
	count := func(n *int64) string {
		if n == nil {
			return "n/a"
		}
		return groupDigits(*n)
	}
	est := "n/a"
	switch {
	case s.rows == nil:
	case *s.rows < 0:
		est = "unknown (not analyzed yet)"
	default:
		est = groupDigits(int64(math.Round(*s.rows)))
	}
	return []driver.DetailRow{
		{Name: "Estimated rows", Definition: est},
		{Name: "Total size", Definition: str(s.total)},
		{Name: "Table size", Definition: str(s.table)},
		{Name: "Index size", Definition: str(s.index)},
		{Name: "Last vacuum", Definition: when(s.vacuum)},
		{Name: "Last autovacuum", Definition: when(s.autovacuum)},
		{Name: "Last analyze", Definition: when(s.analyze)},
		{Name: "Last autoanalyze", Definition: when(s.autoan)},
		{Name: "Seq scans", Definition: count(s.seqScan)},
		{Name: "Index scans", Definition: count(s.idxScan)},
	}
}

// groupDigits formats 1234567 as "1,234,567".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// defaultClause renders the column's default, identity or generation
// clause with a leading space, or "".
func (c colDef) defaultClause() string {
	switch {
	case c.identity == "a":
		return " GENERATED ALWAYS AS IDENTITY"
	case c.identity == "d":
		return " GENERATED BY DEFAULT AS IDENTITY"
	case c.generated == "s":
		return " GENERATED ALWAYS AS (" + c.def + ") STORED"
	case c.generated == "v":
		return " GENERATED ALWAYS AS (" + c.def + ") VIRTUAL"
	case c.def != "":
		return " DEFAULT " + c.def
	}
	return ""
}

// ddl renders a script that recreates the relation: CREATE statement,
// row-level security, indexes that do not back a constraint, triggers and
// comments. Inherited pieces (partition indexes, triggers and constraints
// cloned from the parent, inherited columns) are left out, since creating
// the relation under its parent recreates them.
func (r *relDef) ddl() string {
	var groups [][]string
	switch r.kind {
	case "v", "m":
		groups = append(groups, []string{r.viewDDL()})
	default:
		groups = append(groups, []string{r.tableDDL()})
	}

	var alter []string
	if r.rls {
		alter = append(alter, "ALTER TABLE "+r.qname+" ENABLE ROW LEVEL SECURITY;")
	}
	if r.forced {
		alter = append(alter, "ALTER TABLE "+r.qname+" FORCE ROW LEVEL SECURITY;")
	}
	groups = append(groups, alter)

	var idx []string
	for _, x := range r.idx {
		if !x.constraint && !x.inherited {
			idx = append(idx, x.def+";")
		}
	}
	groups = append(groups, idx)

	var trg []string
	for _, t := range r.trg {
		if t.inherited {
			continue
		}
		trg = append(trg, t.def+";")
		switch t.enabled {
		case "D":
			trg = append(trg, "ALTER TABLE "+r.qname+" DISABLE TRIGGER "+t.qname+";")
		case "R":
			trg = append(trg, "ALTER TABLE "+r.qname+" ENABLE REPLICA TRIGGER "+t.qname+";")
		case "A":
			trg = append(trg, "ALTER TABLE "+r.qname+" ENABLE ALWAYS TRIGGER "+t.qname+";")
		}
	}
	groups = append(groups, trg)

	var com []string
	if r.comment != "" {
		what := map[string]string{"v": "VIEW", "m": "MATERIALIZED VIEW", "f": "FOREIGN TABLE"}[r.kind]
		if what == "" {
			what = "TABLE"
		}
		com = append(com, "COMMENT ON "+what+" "+r.qname+" IS "+literal(r.comment)+";")
	}
	for _, c := range r.cols {
		if c.comment != "" {
			com = append(com, "COMMENT ON COLUMN "+r.qname+"."+c.qname+" IS "+literal(c.comment)+";")
		}
	}
	for _, k := range r.cons {
		if k.local && k.comment != "" {
			com = append(com, "COMMENT ON CONSTRAINT "+k.qname+" ON "+r.qname+" IS "+literal(k.comment)+";")
		}
	}
	for _, x := range r.idx {
		if !x.constraint && !x.inherited && x.comment != "" {
			com = append(com, "COMMENT ON INDEX "+x.qname+" IS "+literal(x.comment)+";")
		}
	}
	for _, t := range r.trg {
		if !t.inherited && t.comment != "" {
			com = append(com, "COMMENT ON TRIGGER "+t.qname+" ON "+r.qname+" IS "+literal(t.comment)+";")
		}
	}
	groups = append(groups, com)

	var out []string
	for _, g := range groups {
		if len(g) > 0 {
			out = append(out, strings.Join(g, "\n"))
		}
	}
	return strings.Join(out, "\n\n") + "\n"
}

func (r *relDef) tableDDL() string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if r.persistence == "u" {
		b.WriteString("UNLOGGED ")
	}
	if r.kind == "f" {
		b.WriteString("FOREIGN ")
	}
	b.WriteString("TABLE " + r.qname)

	var elems []string
	if r.partOf == "" {
		for _, c := range r.cols {
			if !c.local {
				continue
			}
			e := "    " + c.qname + " " + c.typ
			if c.collation != "" {
				e += " COLLATE " + c.collation
			}
			e += c.defaultClause()
			if c.notNull {
				e += " NOT NULL"
			}
			elems = append(elems, e)
		}
	}
	for _, k := range r.cons {
		if k.local {
			elems = append(elems, "    CONSTRAINT "+k.qname+" "+k.def)
		}
	}
	body := " (\n" + strings.Join(elems, ",\n") + "\n)"
	if len(elems) == 0 {
		body = " ()"
	}
	if r.partOf != "" {
		b.WriteString(" PARTITION OF " + r.partOf)
		if len(elems) > 0 {
			b.WriteString(body)
		}
		b.WriteString("\n" + r.partBound)
	} else {
		b.WriteString(body)
	}
	if len(r.inherits) > 0 {
		b.WriteString("\nINHERITS (" + strings.Join(r.inherits, ", ") + ")")
	}
	if r.partKey != "" {
		b.WriteString("\nPARTITION BY " + r.partKey)
	}
	if r.kind == "f" {
		b.WriteString("\nSERVER " + r.server)
		if len(r.ftOptions) > 0 {
			opts := make([]string, len(r.ftOptions))
			for i, o := range r.ftOptions {
				k, v, _ := strings.Cut(o, "=")
				opts[i] = k + " " + literal(v)
			}
			b.WriteString("\nOPTIONS (" + strings.Join(opts, ", ") + ")")
		}
	}
	r.storageClauses(&b)
	b.WriteString(";")
	return b.String()
}

func (r *relDef) storageClauses(b *strings.Builder) {
	if r.am != "" {
		b.WriteString("\nUSING " + r.am)
	}
	if len(r.options) > 0 {
		b.WriteString("\nWITH (" + relOptions(r.options) + ")")
	}
	if r.tablespace != "" {
		b.WriteString("\nTABLESPACE " + r.tablespace)
	}
}

func (r *relDef) viewDDL() string {
	body := strings.TrimRight(strings.TrimSpace(r.viewDef), "; \n")
	var b strings.Builder
	if r.kind == "v" {
		b.WriteString("CREATE VIEW " + r.qname)
		if len(r.options) > 0 {
			b.WriteString(" WITH (" + relOptions(r.options) + ")")
		}
		b.WriteString(" AS\n" + body + ";")
		return b.String()
	}
	b.WriteString("CREATE MATERIALIZED VIEW " + r.qname)
	r.storageClauses(&b)
	b.WriteString(" AS\n" + body + "\nWITH ")
	if !r.populated {
		b.WriteString("NO ")
	}
	b.WriteString("DATA;")
	return b.String()
}

// relOptions renders reloptions ("fillfactor=70") for a WITH clause,
// quoting values that are not plain words or numbers.
func relOptions(opts []string) string {
	out := make([]string, len(opts))
	for i, o := range opts {
		k, v, _ := strings.Cut(o, "=")
		if !plainWord(v) {
			v = literal(v)
		}
		out[i] = k + "=" + v
	}
	return strings.Join(out, ", ")
}

func plainWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// literal quotes s as a SQL string literal (standard_conforming_strings on).
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
