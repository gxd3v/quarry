package oracle

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Describer = (*conn)(nil)

// metadataTypes maps explorer folders to DBMS_METADATA object types.
var metadataTypes = map[string]string{
	folderTables:     "TABLE",
	folderViews:      "VIEW",
	folderMatviews:   "MATERIALIZED_VIEW",
	folderSequences:  "SEQUENCE",
	folderProcedures: "PROCEDURE",
	folderFunctions:  "FUNCTION",
	folderPackages:   "PACKAGE",
	folderTriggers:   "TRIGGER",
	folderSynonyms:   "SYNONYM",
}

// Describe returns columns, indexes, constraints, triggers, statistics and
// DDL for a table, view or materialized view, and the DDL alone for
// sequences, procedures, functions, packages (spec and body), triggers and
// synonyms. The DDL comes from DBMS_METADATA without storage and segment
// clauses; when the user may not read it (other schemas need
// SELECT_CATALOG_ROLE), it is a comment saying why.
func (c *conn) Describe(ctx context.Context, target *driver.Node) (*driver.ObjectDetails, error) {
	if target == nil || len(target.Path) != 3 || metadataTypes[target.Path[1]] == "" {
		return nil, fmt.Errorf("cannot describe this object")
	}
	p := target.Path
	if strings.ContainsAny(p[0]+p[2], "\"\x00") {
		return nil, fmt.Errorf("invalid object name %q", p[0]+"."+p[2])
	}
	schema, name, folder := p[0], p[2], p[1]
	d := &driver.ObjectDetails{}
	err := c.with(ctx, func(l *link) error {
		if folder == folderTables || folder == folderViews || folder == folderMatviews {
			if err := describeRel(ctx, l, d, folder, schema, name); err != nil {
				return err
			}
		}
		d.DDL = objectDDL(ctx, l, metadataTypes[folder], schema, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func describeRel(ctx context.Context, l *link, d *driver.ObjectDetails, folder, schema, name string) error {
	cols, err := columnDefs(ctx, l, schema, name)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("%s.%s does not exist", schema, name)
	}
	for _, c := range cols {
		def := c.defaultClause()
		if strings.EqualFold(def, "NULL") {
			def = ""
		}
		d.Columns = append(d.Columns, driver.ColumnInfo{
			Name: c.name, Type: c.typ, Default: def, Comment: c.comment, Nullable: !c.notNull, PrimaryKey: c.pk,
		})
	}
	if folder == folderViews {
		return nil
	}
	idx, err := indexDefs(ctx, l, schema, name)
	if err != nil {
		return err
	}
	for _, x := range idx {
		d.Indexes = append(d.Indexes, driver.DetailRow{Name: x.name, Definition: x.definition(schema, name)})
	}
	if folder == folderTables {
		cons, err := constraintDefs(ctx, l, schema, name)
		if err != nil {
			return err
		}
		for _, k := range cons {
			d.Constraints = append(d.Constraints, driver.DetailRow{Name: k.name, Definition: k.def})
		}
		if d.Triggers, err = triggerRows(ctx, l, schema, name); err != nil {
			return err
		}
	}
	d.Stats, err = statRows(ctx, l, folder, schema, name)
	return err
}

const tableTriggersQuery = `
SELECT trigger_name, trigger_type, triggering_event, status, when_clause
FROM all_triggers
WHERE table_owner = :1 AND table_name = :2
ORDER BY trigger_name`

func triggerRows(ctx context.Context, l *link, schema, name string) ([]driver.DetailRow, error) {
	var out []driver.DetailRow
	err := l.query(ctx, tableTriggersQuery, []any{schema, name}, func(r []sqldriver.Value) error {
		def := strings.TrimSpace(str(r[1])) + " " + strings.TrimSpace(str(r[2]))
		if w := strings.TrimSpace(str(r[4])); w != "" {
			def += " WHEN (" + w + ")"
		}
		if str(r[3]) == "DISABLED" {
			def += " DISABLED"
		}
		out = append(out, driver.DetailRow{Name: str(r[0]), Definition: def})
		return nil
	})
	return out, err
}

const statsQuery = `
SELECT t.num_rows, t.blocks, t.avg_row_len, t.last_analyzed, t.partitioned, t.temporary,
       m.last_refresh_date, m.staleness
FROM all_tables t
LEFT JOIN all_mviews m ON m.owner = t.owner AND m.mview_name = t.table_name
WHERE t.owner = :1 AND t.table_name = :2`

// segmentsQuery sums the space of a table, its indexes and LOB segments;
// %s is dba_segments (needs privileges) or user_segments, :x binds are
// numbered by renumber.
const segmentsQuery = `
SELECT CASE WHEN s.segment_type LIKE 'INDEX%%' THEN 'I' WHEN s.segment_type LIKE 'LOB%%' THEN 'L' ELSE 'T' END,
       SUM(s.bytes)
FROM %s s
WHERE %s (s.segment_name = :x
       OR s.segment_name IN (SELECT index_name FROM all_indexes WHERE table_owner = :x AND table_name = :x)
       OR s.segment_name IN (SELECT segment_name FROM all_lobs WHERE owner = :x AND table_name = :x))
GROUP BY CASE WHEN s.segment_type LIKE 'INDEX%%' THEN 'I' WHEN s.segment_type LIKE 'LOB%%' THEN 'L' ELSE 'T' END`

func statRows(ctx context.Context, l *link, folder, schema, name string) ([]driver.DetailRow, error) {
	var (
		rows, blocks, avg, part, temp, stale any
		analyzed, refreshed                  *time.Time
		found                                bool
	)
	err := l.query(ctx, statsQuery, []any{schema, name}, func(r []sqldriver.Value) error {
		found = true
		return scanRow(r, &rows, &blocks, &avg, &analyzed, &part, &temp, &refreshed, &stale)
	})
	if err != nil || !found {
		return nil, err
	}
	sizes, known := segmentSizes(ctx, l, schema, name)
	size := func(k string) string {
		if !known {
			return "n/a"
		}
		return prettySize(sizes[k])
	}
	when := func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return t.Format("2006-01-02 15:04:05")
	}
	est := "unknown (not analyzed yet)"
	if rows != nil {
		est = groupDigits(integer(rows))
	}
	out := []driver.DetailRow{
		{Name: "Estimated rows", Definition: est},
		{Name: "Blocks", Definition: orNA(blocks)},
		{Name: "Average row length", Definition: orNA(avg)},
		{Name: "Total size", Definition: size("")},
		{Name: "Table size", Definition: size("T")},
		{Name: "Index size", Definition: size("I")},
		{Name: "LOB size", Definition: size("L")},
		{Name: "Last analyzed", Definition: when(analyzed)},
		{Name: "Partitioned", Definition: strings.ToLower(str(part))},
	}
	if str(temp) == "Y" {
		out = append(out, driver.DetailRow{Name: "Temporary", Definition: "yes"})
	}
	if folder == folderMatviews {
		out = append(out,
			driver.DetailRow{Name: "Last refresh", Definition: when(refreshed)},
			driver.DetailRow{Name: "Staleness", Definition: strings.ToLower(str(stale))})
	}
	return out, nil
}

// segmentSizes returns bytes per kind (T table, I index, L LOB, "" total),
// from dba_segments, or user_segments for the user's own tables.
func segmentSizes(ctx context.Context, l *link, schema, name string) (map[string]int64, bool) {
	sizes := map[string]int64{}
	scan := func(r []sqldriver.Value) error {
		n := integer(r[1])
		sizes[str(r[0])] += n
		sizes[""] += n
		return nil
	}
	args := []any{name, schema, name, schema, name}
	q := renumber(fmt.Sprintf(segmentsQuery, "dba_segments", "s.owner = :x AND"))
	if l.query(ctx, q, append([]any{schema}, args...), scan) == nil {
		return sizes, true
	}
	clear(sizes)
	var user string
	if l.queryRow(ctx, "SELECT USER FROM dual", nil, &user) != nil || user != schema {
		return nil, false
	}
	if l.query(ctx, renumber(fmt.Sprintf(segmentsQuery, "user_segments", "")), args, scan) != nil {
		return nil, false
	}
	return sizes, true
}

var bindRe = regexp.MustCompile(`:x\b`)

// renumber numbers :x binds as :1, :2, ... in order of appearance.
func renumber(q string) string {
	n := 0
	return bindRe.ReplaceAllStringFunc(q, func(string) string {
		n++
		return ":" + strconv.Itoa(n)
	})
}

func orNA(v any) string {
	if v == nil {
		return "n/a"
	}
	return groupDigits(integer(v))
}

// prettySize renders bytes like PostgreSQL's pg_size_pretty.
func prettySize(n int64) string {
	units := []string{"bytes", "kB", "MB", "GB", "TB", "PB"}
	v, i := float64(n), 0
	for v >= 10*1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
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

// ddlBlock assembles an object's DDL with DBMS_METADATA: the object itself,
// then for tables and materialized views the indexes that do not back a
// constraint, then comments and table triggers.
const ddlBlock = `DECLARE
  o VARCHAR2(128) := :1;
  n VARCHAR2(128) := :2;
  t VARCHAR2(30) := :3;
  r CLOB;
  h NUMBER := DBMS_METADATA.SESSION_TRANSFORM;
  FUNCTION dep(k VARCHAR2) RETURN CLOB IS
  BEGIN
    RETURN DBMS_METADATA.GET_DEPENDENT_DDL(k, n, o);
  EXCEPTION
    WHEN OTHERS THEN
      IF SQLCODE = -31608 THEN
        RETURN NULL;
      END IF;
      RAISE;
  END;
BEGIN
  DBMS_METADATA.SET_TRANSFORM_PARAM(h, 'DEFAULT', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(h, 'PRETTY', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(h, 'SQLTERMINATOR', TRUE);
  IF t IN ('TABLE', 'MATERIALIZED_VIEW') THEN
    DBMS_METADATA.SET_TRANSFORM_PARAM(h, 'SEGMENT_ATTRIBUTES', FALSE);
    DBMS_METADATA.SET_TRANSFORM_PARAM(h, 'STORAGE', FALSE);
  END IF;
  r := DBMS_METADATA.GET_DDL(t, n, o);
  IF t IN ('TABLE', 'MATERIALIZED_VIEW') THEN
    FOR i IN (SELECT x.owner, x.index_name FROM all_indexes x
              WHERE x.table_owner = o AND x.table_name = n AND x.index_type <> 'LOB'
                AND x.index_name NOT IN (SELECT k.index_name FROM all_constraints k
                                         WHERE k.owner = o AND k.table_name = n AND k.constraint_type IN ('P', 'U')
                                           AND k.index_name IS NOT NULL)
              ORDER BY x.index_name) LOOP
      r := r || DBMS_METADATA.GET_DDL('INDEX', i.index_name, i.owner);
    END LOOP;
  END IF;
  IF t IN ('TABLE', 'VIEW', 'MATERIALIZED_VIEW') THEN
    r := r || dep('COMMENT');
  END IF;
  IF t = 'TABLE' THEN
    r := r || dep('TRIGGER');
  END IF;
  :4 := r;
END;`

func objectDDL(ctx context.Context, l *link, typ, schema, name string) string {
	var ddl go_ora.Clob
	_, err := l.exec(ctx, ddlBlock, schema, name, typ, sql.Out{Dest: &ddl})
	if err != nil {
		l.check(err)
		msg := err.Error()
		if qe, ok := queryError(err, "").(*driver.QueryError); ok {
			msg = qe.Code + ": " + qe.Msg
		}
		return "-- DDL unavailable: " + firstLine(msg) + "\n"
	}
	return tidyDDL(ddl.String)
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// tidyDDL removes DBMS_METADATA's indentation of top-level statements,
// trailing blanks and runs of empty lines.
func tidyDDL(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " \t")
		if t := strings.TrimLeft(line, " "); topLevel(t) {
			if i > 0 && strings.TrimSpace(lines[i-1]) != "" {
				line = "\n" + t
			} else {
				line = t
			}
		}
		lines[i] = line
	}
	out := blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(out) + "\n"
}

func topLevel(line string) bool {
	for _, p := range []string{"CREATE ", "ALTER ", "COMMENT ON ", "GRANT "} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}
