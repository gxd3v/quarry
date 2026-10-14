package mongo

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Describer = (*conn)(nil)

// Describe returns what the object viewer shows for a collection or view:
//
//   - Columns: the top-level fields of up to 100 sampled documents (_id is
//     the primary key; Nullable when a field is missing or null somewhere).
//   - Stats ($collStats): documents, data size, storage size, average
//     document size, indexes, index size, capped; for views, the source
//     and pipeline.
//   - Indexes: name → key pattern and options.
//   - Constraints: the validator, validationLevel and validationAction.
//   - DDL: a db.createCollection (or db.createView) call followed by one
//     createIndex per index other than _id_.
func (c *conn) Describe(ctx context.Context, target *driver.Node) (*driver.ObjectDetails, error) {
	db, coll, err := collectionNode(target)
	if err != nil {
		return nil, err
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	ci, err := c.collection(ctx, db, coll)
	if err != nil {
		return nil, err
	}
	d := &driver.ObjectDetails{}
	if fields, err := c.sample(ctx, db, coll); err == nil {
		for _, f := range fields {
			d.Columns = append(d.Columns, driver.ColumnInfo{Name: f.name, Type: f.typ, Nullable: f.nullable, PrimaryKey: f.name == "_id"})
		}
	}
	if ci.Type == "view" {
		on, _ := ci.Options.Lookup("viewOn").StringValueOK()
		pipeline := ci.Options.Lookup("pipeline")
		d.Stats = []driver.DetailRow{{Name: "View on", Definition: on}, {Name: "Pipeline", Definition: formatJSIndent(pipeline)}}
		d.DDL = fmt.Sprintf("db.createView(%s, %s, %s)\n", jsQuote(coll), jsQuote(on), formatJSIndent(pipeline))
		return d, nil
	}
	indexes, err := c.listIndexes(ctx, db, coll)
	if err != nil {
		return nil, queryError(err)
	}
	for _, ix := range indexes {
		def := formatJS(ix.Key)
		if opts := ix.options(); len(docKeys(opts)) > 1 {
			def += " " + formatJS(filterDoc(opts, func(k string) bool { return k != "name" }))
		}
		d.Indexes = append(d.Indexes, driver.DetailRow{Name: ix.Name, Definition: def})
	}
	d.Stats = append([]driver.DetailRow{{Name: "Type", Definition: ci.Type}}, c.collStats(ctx, db, coll)...)
	for _, k := range []string{"validator", "validationLevel", "validationAction"} {
		if v, err := ci.Options.LookupErr(k); err == nil {
			def := formatJSIndent(v)
			if s, ok := v.StringValueOK(); ok {
				def = s
			}
			d.Constraints = append(d.Constraints, driver.DetailRow{Name: k, Definition: def})
		}
	}
	d.DDL = ddl(coll, ci.Options, indexes)
	return d, nil
}

// collStats reads storage statistics; it returns nothing when the user may
// not read them.
func (c *conn) collStats(ctx context.Context, db, coll string) []driver.DetailRow {
	cmd := bson.D{
		{Key: "aggregate", Value: coll},
		{Key: "pipeline", Value: bson.A{bson.D{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}}}}}},
		{Key: "cursor", Value: bson.D{}},
	}
	cur, err := c.client.Database(db).RunCommandCursor(ctx, cmd)
	if err != nil {
		return nil
	}
	defer cur.Close(context.WithoutCancel(ctx))
	if !cur.Next(ctx) {
		return nil
	}
	st, ok := cur.Current.Lookup("storageStats").DocumentOK()
	if !ok {
		return nil
	}
	num := func(k string) (int64, bool) {
		v, err := st.LookupErr(k)
		if err != nil {
			return 0, false
		}
		return v.AsInt64OK()
	}
	var rows []driver.DetailRow
	add := func(name, key string, format func(int64) string) {
		if n, ok := num(key); ok {
			rows = append(rows, driver.DetailRow{Name: name, Definition: format(n)})
		}
	}
	count := func(n int64) string { return strconv.FormatInt(n, 10) }
	add("Documents", "count", count)
	add("Data size", "size", size)
	add("Storage size", "storageSize", size)
	add("Average document size", "avgObjSize", size)
	add("Indexes", "nindexes", count)
	add("Total index size", "totalIndexSize", size)
	capped, _ := st.Lookup("capped").BooleanOK()
	rows = append(rows, driver.DetailRow{Name: "Capped", Definition: strconv.FormatBool(capped)})
	return rows
}

// ddl writes the script that recreates a collection and its indexes.
func ddl(coll string, opts bson.Raw, indexes []indexSpec) string {
	var b strings.Builder
	b.WriteString("db.createCollection(" + jsQuote(coll))
	if len(docKeys(opts)) > 0 {
		b.WriteString(", " + formatJSIndent(opts))
	}
	b.WriteString(")\n")
	for _, ix := range indexes {
		if ix.Name == "_id_" {
			continue
		}
		fmt.Fprintf(&b, "db.getCollection(%s).createIndex(%s, %s)\n", jsQuote(coll), formatJS(ix.Key), formatJS(ix.options()))
	}
	return b.String()
}
