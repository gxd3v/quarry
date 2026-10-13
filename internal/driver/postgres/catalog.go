package postgres

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

// Explorer tree paths: [db, schema, folderKey, object, subFolderKey, item].
const (
	folderTables      = "tables"
	folderViews       = "views"
	folderMatviews    = "matviews"
	folderFunctions   = "functions"
	folderSequences   = "sequences"
	folderColumns     = "columns"
	folderIndexes     = "indexes"
	folderConstraints = "constraints"
)

func (c *conn) Children(ctx context.Context, parent *driver.Node) ([]*driver.Node, error) {
	if parent == nil {
		return c.databases(ctx)
	}
	p := parent.Path
	switch len(p) {
	case 1:
		return c.schemas(ctx, p)
	case 2:
		return c.folders(ctx, p)
	case 3:
		return c.objects(ctx, p)
	case 4:
		return c.subFolders(ctx, p)
	case 5:
		return c.items(ctx, p)
	}
	return nil, nil
}

func child(parent []string, key string, kind driver.NodeKind, name, detail string, leaf bool) *driver.Node {
	path := append(parent[:len(parent):len(parent)], key)
	return &driver.Node{Path: path, Kind: kind, Name: name, Detail: detail, Leaf: leaf}
}

// list runs a catalog query on db's metadata connection, calling scan per row.
func (c *conn) list(ctx context.Context, db, sql string, args []any, scan func(pgx.Rows) error) error {
	return c.with(ctx, db, func(pc *pgx.Conn) error {
		rows, err := pc.Query(ctx, sql, args...)
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
	})
}

func (c *conn) databases(ctx context.Context) ([]*driver.Node, error) {
	const q = `
SELECT datname,
       CASE WHEN has_database_privilege(oid, 'CONNECT')
            THEN pg_size_pretty(pg_database_size(oid)) ELSE '' END
FROM pg_database
WHERE NOT datistemplate AND datallowconn
ORDER BY datname`
	var out []*driver.Node
	err := c.list(ctx, "", q, nil, func(r pgx.Rows) error {
		var name, size string
		if err := r.Scan(&name, &size); err != nil {
			return err
		}
		out = append(out, child(nil, name, driver.NodeDatabase, name, size, false))
		return nil
	})
	return out, err
}

func (c *conn) schemas(ctx context.Context, p []string) ([]*driver.Node, error) {
	const q = `
SELECT nspname, nspname IN ('pg_catalog', 'information_schema') AS sys
FROM pg_namespace
WHERE nspname !~ '^pg_(toast|temp_)'
ORDER BY sys, nspname`
	var out []*driver.Node
	err := c.list(ctx, p[0], q, nil, func(r pgx.Rows) error {
		var name string
		var sys bool
		if err := r.Scan(&name, &sys); err != nil {
			return err
		}
		detail := ""
		if sys {
			detail = "system"
		}
		out = append(out, child(p, name, driver.NodeSchema, name, detail, false))
		return nil
	})
	return out, err
}

func (c *conn) folders(ctx context.Context, p []string) ([]*driver.Node, error) {
	const q = `
SELECT count(*) FILTER (WHERE c.relkind IN ('r', 'p', 'f') AND NOT c.relispartition),
       count(*) FILTER (WHERE c.relkind = 'v'),
       count(*) FILTER (WHERE c.relkind = 'm'),
       (SELECT count(*) FROM pg_proc p WHERE p.pronamespace = n.oid AND p.prokind IN ('f', 'p')),
       count(*) FILTER (WHERE c.relkind = 'S')
FROM pg_namespace n
LEFT JOIN pg_class c ON c.relnamespace = n.oid
WHERE n.nspname = $1
GROUP BY n.oid`
	var out []*driver.Node
	err := c.list(ctx, p[0], q, []any{p[1]}, func(r pgx.Rows) error {
		var n [5]int64
		if err := r.Scan(&n[0], &n[1], &n[2], &n[3], &n[4]); err != nil {
			return err
		}
		folders := []struct{ key, name string }{
			{folderTables, "Tables"},
			{folderViews, "Views"},
			{folderMatviews, "Materialized views"},
			{folderFunctions, "Functions"},
			{folderSequences, "Sequences"},
		}
		for i, f := range folders {
			if n[i] > 0 || f.key == folderTables {
				out = append(out, child(p, f.key, driver.NodeFolder, f.name, fmt.Sprintf("(%d)", n[i]), false))
			}
		}
		return nil
	})
	return out, err
}

func (c *conn) objects(ctx context.Context, p []string) ([]*driver.Node, error) {
	db, schema := p[0], p[1]
	var out []*driver.Node
	switch p[2] {
	case folderTables, folderViews, folderMatviews, folderSequences:
		const q = `
SELECT c.relname, c.relkind::text, c.reltuples::float8
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = ANY(string_to_array($2, ',')::"char"[]) AND NOT c.relispartition
ORDER BY c.relname`
		kinds := map[string]string{
			folderTables:    "r,p,f",
			folderViews:     "v",
			folderMatviews:  "m",
			folderSequences: "S",
		}[p[2]]
		err := c.list(ctx, db, q, []any{schema, kinds}, func(r pgx.Rows) error {
			var name, kind string
			var tuples float64
			if err := r.Scan(&name, &kind, &tuples); err != nil {
				return err
			}
			var n *driver.Node
			switch kind {
			case "r":
				n = child(p, name, driver.NodeTable, name, rowEstimate(tuples), false)
			case "p":
				n = child(p, name, driver.NodeTable, name, "partitioned", false)
			case "f":
				n = child(p, name, driver.NodeTable, name, "foreign", false)
			case "v":
				n = child(p, name, driver.NodeView, name, "", false)
			case "m":
				n = child(p, name, driver.NodeView, name, "materialized", false)
			case "S":
				n = child(p, name, driver.NodeSequence, name, "", true)
			default:
				return nil
			}
			out = append(out, n)
			return nil
		})
		return out, err
	case folderFunctions:
		const q = `
SELECT p.proname, oidvectortypes(p.proargtypes), coalesce(pg_get_function_result(p.oid), '')
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.prokind IN ('f', 'p')
ORDER BY 1, 2`
		err := c.list(ctx, db, q, []any{schema}, func(r pgx.Rows) error {
			var name, args, result string
			if err := r.Scan(&name, &args, &result); err != nil {
				return err
			}
			detail := "(" + args + ")"
			if result != "" {
				detail += " → " + result
			}
			out = append(out, child(p, name+"("+args+")", driver.NodeFunction, name, detail, true))
			return nil
		})
		return out, err
	}
	return nil, nil
}

func (c *conn) subFolders(ctx context.Context, p []string) ([]*driver.Node, error) {
	if p[2] != folderTables && p[2] != folderViews && p[2] != folderMatviews {
		return nil, nil
	}
	const q = `
SELECT (SELECT count(*) FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped),
       (SELECT count(*) FROM pg_index i WHERE i.indrelid = c.oid),
       (SELECT count(*) FROM pg_constraint k WHERE k.conrelid = c.oid AND k.contype <> 'n')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`
	var out []*driver.Node
	err := c.list(ctx, p[0], q, []any{p[1], p[3]}, func(r pgx.Rows) error {
		var cols, idx, cons int64
		if err := r.Scan(&cols, &idx, &cons); err != nil {
			return err
		}
		out = append(out, child(p, folderColumns, driver.NodeFolder, "Columns", fmt.Sprintf("(%d)", cols), false))
		if p[2] == folderTables {
			out = append(out,
				child(p, folderIndexes, driver.NodeFolder, "Indexes", fmt.Sprintf("(%d)", idx), false),
				child(p, folderConstraints, driver.NodeFolder, "Constraints", fmt.Sprintf("(%d)", cons), false))
		}
		return nil
	})
	return out, err
}

func (c *conn) items(ctx context.Context, p []string) ([]*driver.Node, error) {
	db, args := p[0], []any{p[1], p[3]}
	var out []*driver.Node
	switch p[4] {
	case folderColumns:
		const q = `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
       coalesce(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity::text, a.attgenerated::text
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`
		err := c.list(ctx, db, q, args, func(r pgx.Rows) error {
			var name, typ, def, identity, generated string
			var notNull bool
			if err := r.Scan(&name, &typ, &notNull, &def, &identity, &generated); err != nil {
				return err
			}
			out = append(out, child(p, name, driver.NodeColumn, name, columnDetail(typ, notNull, def, identity, generated), true))
			return nil
		})
		return out, err
	case folderIndexes:
		const q = `
SELECT i.relname, pg_get_indexdef(x.indexrelid), x.indisprimary, x.indisunique
FROM pg_index x
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_class c ON c.oid = x.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY x.indisprimary DESC, i.relname`
		err := c.list(ctx, db, q, args, func(r pgx.Rows) error {
			var name, def string
			var primary, unique bool
			if err := r.Scan(&name, &def, &primary, &unique); err != nil {
				return err
			}
			out = append(out, child(p, name, driver.NodeIndex, name, indexDetail(def, primary, unique), true))
			return nil
		})
		return out, err
	case folderConstraints:
		const q = `
SELECT k.conname, pg_get_constraintdef(k.oid)
FROM pg_constraint k
JOIN pg_class c ON c.oid = k.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND k.contype <> 'n'
ORDER BY array_position(ARRAY['p', 'u', 'f', 'c', 'x', 't']::"char"[], k.contype), k.conname`
		err := c.list(ctx, db, q, args, func(r pgx.Rows) error {
			var name, def string
			if err := r.Scan(&name, &def); err != nil {
				return err
			}
			out = append(out, child(p, name, driver.NodeIndex, name, def, true))
			return nil
		})
		return out, err
	}
	return nil, nil
}

// rowEstimate renders pg_class.reltuples like "~12k"; -1 means never
// analyzed.
func rowEstimate(n float64) string {
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

func columnDetail(typ string, notNull bool, def, identity, generated string) string {
	s := typ
	if notNull {
		s += " not null"
	}
	switch {
	case identity != "":
		s += " identity"
	case generated != "":
		s += " generated"
	case def != "":
		s += " = " + shortDefault(def)
	}
	return s
}

var castRe = regexp.MustCompile(`::(?:"[^"]+"|character varying|double precision|time(?:stamp)? with(?:out)? time zone|[a-z_][a-z0-9_]*)(?:\[\])*`)

// shortDefault drops redundant casts (nextval('s'::regclass) → nextval('s'))
// and truncates long expressions.
func shortDefault(def string) string {
	def = castRe.ReplaceAllString(def, "")
	const maxLen = 32
	if r := []rune(def); len(r) > maxLen {
		return string(r[:maxLen-1]) + "…"
	}
	return def
}

// indexDetail turns "CREATE UNIQUE INDEX i ON t USING btree (a)" into
// "btree (a) unique".
func indexDetail(def string, primary, unique bool) string {
	if i := strings.Index(def, " USING "); i >= 0 {
		def = def[i+len(" USING "):]
	}
	switch {
	case primary:
		def += " primary"
	case unique:
		def += " unique"
	}
	return def
}
