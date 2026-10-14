package oracle

import (
	"cmp"
	"context"
	sqldriver "database/sql/driver"
	"slices"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Catalog = (*conn)(nil)

// catalogOwners are the schemas autocomplete covers: every schema not
// maintained by Oracle, the current user's and the scope's (:1).
const catalogOwners = `(SELECT username FROM all_users WHERE oracle_maintained = 'N'
  UNION SELECT USER FROM dual
  UNION SELECT :1 FROM dual)`

const (
	catalogColumnsQuery = `
SELECT c.owner, c.table_name, c.column_name, c.data_type, c.data_length, c.data_precision, c.data_scale,
       c.char_length, c.char_used, c.data_type_owner, c.nullable
FROM all_tab_columns c
WHERE c.owner IN ` + catalogOwners + ` AND c.table_name NOT LIKE 'BIN$%'
ORDER BY c.owner, c.table_name, c.column_id`

	catalogViewsQuery = `
SELECT owner, view_name, 'V' FROM all_views WHERE owner IN ` + catalogOwners + `
UNION ALL
SELECT owner, mview_name, 'M' FROM all_mviews WHERE owner IN ` + catalogOwners

	catalogRoutinesQuery = `
SELECT o.owner, o.object_name, o.object_type, a.args, a.ret
FROM all_objects o
LEFT JOIN (
	SELECT owner, object_name,
	       LISTAGG(CASE WHEN position > 0 AND argument_name IS NOT NULL THEN
	                 argument_name || CASE in_out WHEN 'IN' THEN '' ELSE ' ' || REPLACE(in_out, '/', ' ') END || ' ' || data_type
	               END, ', ') WITHIN GROUP (ORDER BY position) AS args,
	       MAX(CASE WHEN position = 0 THEN data_type END) AS ret
	FROM all_arguments
	WHERE owner IN ` + catalogOwners + ` AND package_name IS NULL AND data_level = 0
	GROUP BY owner, object_name
) a ON a.owner = o.owner AND a.object_name = o.object_name
WHERE o.owner IN ` + catalogOwners + ` AND o.object_type IN ('PROCEDURE', 'FUNCTION', 'PACKAGE', 'SEQUENCE')
  AND o.object_name NOT LIKE 'ISEQ$$%'`
)

// Objects lists the tables, views, materialized views, procedures,
// functions, packages and sequences of every schema not maintained by
// Oracle, plus the current user's and scope.Schema; scope.Database is
// ignored. Results are ordered by schema then name, relations before
// routines of the same name.
func (c *conn) Objects(ctx context.Context, scope driver.Scope) ([]driver.Object, error) {
	var out []driver.Object
	err := c.with(ctx, func(l *link) error {
		out = out[:0]
		views := map[[2]string]string{}
		err := l.query(ctx, catalogViewsQuery, []any{scope.Schema, scope.Schema}, func(r []sqldriver.Value) error {
			views[[2]string{str(r[0]), str(r[1])}] = str(r[2])
			return nil
		})
		if err != nil {
			return err
		}
		err = l.query(ctx, catalogColumnsQuery, []any{scope.Schema}, func(r []sqldriver.Value) error {
			schema, name := str(r[0]), str(r[1])
			if n := len(out); n == 0 || out[n-1].Schema != schema || out[n-1].Name != name {
				o := driver.Object{Schema: schema, Name: name, Kind: driver.NodeTable}
				switch views[[2]string{schema, name}] {
				case "V":
					o.Kind = driver.NodeView
				case "M":
					o.Kind, o.Detail = driver.NodeView, "materialized"
				}
				out = append(out, o)
			}
			o := &out[len(out)-1]
			o.Columns = append(o.Columns, driver.Column{Name: str(r[2]), Type: typeOf(r[3:10]), Nullable: str(r[10]) != "N"})
			return nil
		})
		if err != nil {
			return err
		}
		return l.query(ctx, catalogRoutinesQuery, []any{scope.Schema, scope.Schema}, func(r []sqldriver.Value) error {
			o := driver.Object{Schema: str(r[0]), Name: str(r[1]), Kind: driver.NodeFunction}
			switch str(r[2]) {
			case "SEQUENCE":
				o.Kind = driver.NodeSequence
			case "PACKAGE":
				o.Detail = "package"
			default:
				o.Detail = signature(r[3], r[4])
			}
			out = append(out, o)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	rank := func(k driver.NodeKind) int {
		if k == driver.NodeTable || k == driver.NodeView {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(out, func(a, b driver.Object) int {
		return cmp.Or(strings.Compare(a.Schema, b.Schema), strings.Compare(a.Name, b.Name), rank(a.Kind)-rank(b.Kind))
	})
	return out, nil
}
