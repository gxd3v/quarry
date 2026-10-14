package mongo

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/gxd3v/quarry/internal/driver"
)

const (
	sampleSize    = 100
	sampleTimeout = 5 * time.Second
	// maxSampled bounds how many collections Objects samples for fields.
	maxSampled = 200
)

var _ driver.Catalog = (*conn)(nil)

func authorizedDatabases() *options.ListDatabasesOptionsBuilder {
	return options.ListDatabases().SetAuthorizedDatabases(true)
}

// collInfo is a listCollections entry.
type collInfo struct {
	Name    string   `bson:"name"`
	Type    string   `bson:"type"` // collection, view, timeseries
	Options bson.Raw `bson:"options"`
}

func (ci collInfo) kind() driver.NodeKind {
	if ci.Type == "view" {
		return driver.NodeView
	}
	return driver.NodeCollection
}

func (ci collInfo) detail() string {
	switch {
	case ci.Type == "view":
		on, _ := ci.Options.Lookup("viewOn").StringValueOK()
		return "view on " + on
	case ci.Type == "timeseries":
		return "time series"
	}
	if capped, _ := ci.Options.Lookup("capped").BooleanOK(); capped {
		return "capped"
	}
	return ""
}

// listCollections returns db's collections and views sorted by name.
func (c *conn) listCollections(ctx context.Context, db string) ([]collInfo, error) {
	return c.findCollections(ctx, db, bson.D{})
}

func (c *conn) findCollections(ctx context.Context, db string, filter bson.D) ([]collInfo, error) {
	cur, err := c.client.Database(db).ListCollections(ctx, filter, options.ListCollections().SetAuthorizedCollections(true))
	if err != nil {
		return nil, err
	}
	var out []collInfo
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b collInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// collection looks up one collection or view.
func (c *conn) collection(ctx context.Context, db, name string) (collInfo, error) {
	list, err := c.findCollections(ctx, db, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return collInfo{}, queryError(err)
	}
	if len(list) == 0 {
		return collInfo{}, fmt.Errorf("collection %s.%s does not exist", db, name)
	}
	return list[0], nil
}

// Children lists databases (parent nil), a database's collections and
// views, or a collection's indexes. See the package documentation for the
// paths.
func (c *conn) Children(ctx context.Context, parent *driver.Node) ([]*driver.Node, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	var (
		nodes []*driver.Node
		err   error
	)
	switch {
	case parent == nil:
		nodes, err = c.databases(ctx)
	case len(parent.Path) == 1:
		nodes, err = c.collections(ctx, parent.Path[0])
	case len(parent.Path) == 2:
		nodes, err = c.indexes(ctx, parent.Path[0], parent.Path[1])
	}
	return nodes, queryError(err)
}

func (c *conn) databases(ctx context.Context) ([]*driver.Node, error) {
	res, err := c.client.ListDatabases(ctx, bson.D{}, authorizedDatabases())
	if err != nil {
		return nil, err
	}
	var nodes []*driver.Node
	seen := false
	for _, d := range res.Databases {
		seen = seen || d.Name == c.defaultDB
		nodes = append(nodes, &driver.Node{Path: []string{d.Name}, Kind: driver.NodeDatabase, Name: d.Name, Detail: size(d.SizeOnDisk)})
	}
	if !seen {
		nodes = append(nodes, &driver.Node{Path: []string{c.defaultDB}, Kind: driver.NodeDatabase, Name: c.defaultDB, Detail: "empty"})
	}
	slices.SortFunc(nodes, func(a, b *driver.Node) int { return strings.Compare(a.Name, b.Name) })
	return nodes, nil
}

func (c *conn) collections(ctx context.Context, db string) ([]*driver.Node, error) {
	list, err := c.listCollections(ctx, db)
	if err != nil {
		return nil, err
	}
	nodes := make([]*driver.Node, len(list))
	for i, ci := range list {
		nodes[i] = &driver.Node{Path: []string{db, ci.Name}, Kind: ci.kind(), Name: ci.Name, Detail: ci.detail(), Leaf: ci.Type == "view"}
	}
	return nodes, nil
}

// indexSpec is a listIndexes entry.
type indexSpec struct {
	Name string   `bson:"name"`
	Key  bson.Raw `bson:"key"`
	Raw  bson.Raw `bson:"-"`
}

func (c *conn) listIndexes(ctx context.Context, db, coll string) ([]indexSpec, error) {
	cur, err := c.client.Database(db).RunCommandCursor(ctx, bson.D{{Key: "listIndexes", Value: coll}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(context.WithoutCancel(ctx))
	var out []indexSpec
	for cur.Next(ctx) {
		var ix indexSpec
		if err := cur.Decode(&ix); err != nil {
			return nil, err
		}
		ix.Raw = slices.Clone(cur.Current)
		out = append(out, ix)
	}
	return out, cur.Err()
}

func (c *conn) indexes(ctx context.Context, db, coll string) ([]*driver.Node, error) {
	list, err := c.listIndexes(ctx, db, coll)
	if err != nil {
		return nil, err
	}
	nodes := make([]*driver.Node, len(list))
	for i, ix := range list {
		nodes[i] = &driver.Node{Path: []string{db, coll, ix.Name}, Kind: driver.NodeIndex, Name: ix.Name, Detail: ix.detail(), Leaf: true}
	}
	return nodes, nil
}

// detail summarises an index: its key pattern and notable options.
func (ix indexSpec) detail() string {
	parts := []string{formatJS(ix.Key)}
	for _, f := range []string{"unique", "sparse", "hidden"} {
		if b, _ := ix.Raw.Lookup(f).BooleanOK(); b {
			parts = append(parts, f)
		}
	}
	if v, err := ix.Raw.LookupErr("expireAfterSeconds"); err == nil {
		parts = append(parts, fmt.Sprintf("TTL %ds", v.AsInt64()))
	}
	if _, err := ix.Raw.LookupErr("partialFilterExpression"); err == nil {
		parts = append(parts, "partial")
	}
	return strings.Join(parts, " ")
}

// options returns an index's options for createIndex: everything but v,
// key and ns.
func (ix indexSpec) options() bson.Raw {
	return filterDoc(ix.Raw, func(k string) bool { return k != "v" && k != "key" && k != "ns" })
}

// Objects lists the collections and views of scope.Database (default: the
// connection's database) for autocomplete. Collections get their top-level
// fields, sampled from up to 100 random documents; views (whose pipelines
// may be expensive) and collections beyond the first 200 get none.
// Sampling failures leave a collection without fields.
func (c *conn) Objects(ctx context.Context, scope driver.Scope) ([]driver.Object, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	db := cmp.Or(scope.Database, c.defaultDB)
	ctx, cancel := bound(ctx)
	defer cancel()
	list, err := c.listCollections(ctx, db)
	if err != nil {
		return nil, queryError(err)
	}
	objs := make([]driver.Object, len(list))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, ci := range list {
		objs[i] = driver.Object{Schema: db, Name: ci.Name, Kind: ci.kind(), Detail: ci.detail()}
		if ci.Type == "view" || i >= maxSampled {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			fields, err := c.sample(ctx, db, ci.Name)
			if err != nil {
				return
			}
			for _, f := range fields {
				objs[i].Columns = append(objs[i].Columns, driver.Column{Name: f.name, Type: f.typ, Nullable: f.nullable})
			}
		})
	}
	wg.Wait()
	return objs, nil
}

// field is a top-level field seen in sampled documents.
type field struct {
	name     string
	typ      string // $type alias, "mixed" when it varies
	nullable bool   // missing from some documents or null in some
}

// sample returns the top-level fields of up to sampleSize random documents
// of a collection.
func (c *conn) sample(ctx context.Context, db, coll string) ([]field, error) {
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	cmd := bson.D{
		{Key: "aggregate", Value: coll},
		{Key: "pipeline", Value: bson.A{bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: sampleSize}}}}}},
		{Key: "cursor", Value: bson.D{}},
	}
	cur, err := c.client.Database(db).RunCommandCursor(ctx, cmd)
	if err != nil {
		return nil, err
	}
	defer cur.Close(context.WithoutCancel(ctx))
	var docs []bson.Raw
	for cur.Next(ctx) {
		docs = append(docs, slices.Clone(cur.Current))
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return fieldsOf(docs), nil
}

// fieldsOf lists the top-level fields of docs: _id first, then in order of
// first appearance.
func fieldsOf(docs []bson.Raw) []field {
	var out []field
	index := map[string]int{}
	seen := map[string]int{}
	for _, d := range docs {
		els, _ := d.Elements()
		for _, e := range els {
			k, t := e.Key(), typeName(e.Value())
			i, ok := index[k]
			if !ok {
				i = len(out)
				index[k] = i
				out = append(out, field{name: k})
			}
			seen[k]++
			out[i].typ = mergeType(out[i].typ, t)
			if t == "null" || t == "undefined" {
				out[i].nullable = true
			}
		}
	}
	for i := range out {
		if seen[out[i].name] < len(docs) {
			out[i].nullable = true
		}
	}
	if i, ok := index["_id"]; ok && i > 0 {
		id := out[i]
		out = append([]field{id}, slices.Delete(out, i, i+1)...)
	}
	return out
}

func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f, exp := float64(n), 0
	for f >= unit && exp < 5 {
		f /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", f, "KMGTP"[exp-1])
}
