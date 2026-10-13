package postgres

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Catalog = (*conn)(nil)

// userSchemas filters pg_namespace n down to the schemas autocomplete cares
// about: no system catalogs, TOAST or temporary schemas.
const userSchemas = `n.nspname <> ALL ('{pg_catalog,information_schema}') AND n.nspname !~ '^pg_(toast|temp_)'`

// One row per relation, its columns aggregated into parallel arrays: far
// fewer rows than a relation × column join, and arrays decode cheaply.
// Partitions are left out (as in the explorer): they repeat their parent's
// columns and can number in the thousands.
const relationsQuery = `
SELECT n.nspname, c.relname, c.relkind::text,
       coalesce(a.names, '{}'), coalesce(a.types, '{}'), coalesce(a.notnull, '{}')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN LATERAL (
	SELECT array_agg(a.attname::text ORDER BY a.attnum) AS names,
	       array_agg(format_type(a.atttypid, a.atttypmod) ORDER BY a.attnum) AS types,
	       array_agg(a.attnotnull ORDER BY a.attnum) AS notnull
	FROM pg_attribute a
	WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
) a ON true
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition AND ` + userSchemas + `
ORDER BY 1, 2`

const routinesQuery = `
SELECT n.nspname, p.proname, 'f', oidvectortypes(p.proargtypes), coalesce(pg_get_function_result(p.oid), '')
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE p.prokind IN ('f', 'p') AND ` + userSchemas + `
UNION ALL
SELECT n.nspname, c.relname, 'S', '', ''
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'S' AND ` + userSchemas + `
ORDER BY 1, 2, 4`

// Objects lists the tables, views, functions and sequences of every
// non-system schema of scope.Database (the connection's default database
// when empty); scope.Schema is ignored, since qualified names may point
// anywhere. Results are ordered by schema then name, relations before
// routines of the same name; overloaded functions appear once per signature.
// Both catalog queries go out in a single pipelined round trip.
func (c *conn) Objects(ctx context.Context, scope driver.Scope) ([]driver.Object, error) {
	var out []driver.Object
	err := c.with(ctx, scope.Database, func(pc *pgx.Conn) error {
		out = out[:0]
		b := &pgx.Batch{}
		b.Queue(relationsQuery)
		b.Queue(routinesQuery)
		br := pc.SendBatch(ctx, b)
		err := scanRelations(br, &out)
		if err == nil {
			err = scanRoutines(br, &out)
		}
		if cerr := br.Close(); err == nil {
			err = cerr
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	// Each query is already sorted; merge the two runs. PostgreSQL's name
	// type sorts with the "C" collation, i.e. byte order, like Go strings.
	slices.SortStableFunc(out, func(a, b driver.Object) int {
		return cmp.Or(strings.Compare(a.Schema, b.Schema), strings.Compare(a.Name, b.Name))
	})
	return out, nil
}

func scanRelations(br pgx.BatchResults, out *[]driver.Object) error {
	rows, err := br.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		schema, name, kind string
		names, types       []string
		notNull            []bool
	)
	for rows.Next() {
		if err := rows.Scan(&schema, &name, &kind, &names, &types, &notNull); err != nil {
			return err
		}
		o := driver.Object{Schema: schema, Name: name, Kind: driver.NodeTable}
		switch kind {
		case "p":
			o.Detail = "partitioned"
		case "f":
			o.Detail = "foreign"
		case "v":
			o.Kind = driver.NodeView
		case "m":
			o.Kind, o.Detail = driver.NodeView, "materialized"
		}
		o.Columns = make([]driver.Column, len(names))
		for i := range names {
			o.Columns[i] = driver.Column{Name: names[i], Type: types[i], Nullable: !notNull[i]}
		}
		*out = append(*out, o)
	}
	return rows.Err()
}

func scanRoutines(br pgx.BatchResults, out *[]driver.Object) error {
	rows, err := br.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	var schema, name, kind, args, result string
	for rows.Next() {
		if err := rows.Scan(&schema, &name, &kind, &args, &result); err != nil {
			return err
		}
		o := driver.Object{Schema: schema, Name: name, Kind: driver.NodeSequence}
		if kind == "f" {
			o.Kind, o.Detail = driver.NodeFunction, "("+args+")"
			if result != "" {
				o.Detail += " → " + result
			}
		}
		*out = append(*out, o)
	}
	return rows.Err()
}
