package mongo

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

// Integration tests run against the server named by QUARRY_TEST_MONGO_DSN,
// e.g. mongodb://localhost:27117/?directConnection=true (a single-node
// replica set, which transactions need). They use the database quarry_it and
// drop it when done.

const itDB = "quarry_it"

func itConfig(t testing.TB, readOnly bool) driver.Config {
	t.Helper()
	dsn := os.Getenv("QUARRY_TEST_MONGO_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_MONGO_DSN not set")
	}
	return driver.Config{Kind: driver.MongoDB, Database: itDB, ReadOnly: readOnly, Options: map[string]string{"uri": dsn}}
}

func itOpen(t testing.TB, readOnly bool) driver.Conn {
	t.Helper()
	c, err := New().Open(context.Background(), itConfig(t, readOnly), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// itSetup opens a connection with a fresh quarry_it database.
func itSetup(t *testing.T) (driver.Conn, driver.Session) {
	t.Helper()
	c := itOpen(t, false)
	s := itSession(t, c)
	run(t, s, "db.dropDatabase()")
	t.Cleanup(func() {
		s.Close() // an open transaction would block the drop
		s2, err := c.NewSession(context.Background(), driver.Scope{Database: itDB})
		if err == nil {
			s2.Execute(context.Background(), driver.Statement{Text: "db.dropDatabase()"})
			s2.Close()
		}
	})
	return c, s
}

func itSession(t *testing.T, c driver.Conn) driver.Session {
	t.Helper()
	s, err := c.NewSession(context.Background(), driver.Scope{Database: itDB})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// run executes text and returns its columns and every row.
func run(t *testing.T, s driver.Session, text string) ([]driver.Column, [][]any) {
	t.Helper()
	ctx := context.Background()
	r, err := s.Execute(ctx, driver.Statement{Text: text})
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	defer r.Close()
	var rows [][]any
	for {
		page, err := r.Next(ctx, 100)
		if err != nil {
			t.Fatalf("%s: next: %v", text, err)
		}
		if len(page) == 0 {
			return r.Columns(), rows
		}
		rows = append(rows, page...)
	}
}

func colIndex(cols []driver.Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func TestITQueries(t *testing.T) {
	_, s := itSetup(t)
	run(t, s, `db.people.insertMany([
		{_id: 1, name: "ann", age: 31, tags: ["a", "b"], born: ISODate("1993-04-01T00:00:00Z")},
		{_id: 2, name: "bob", age: NumberLong(25), nested: {x: 1}},
		{_id: 3, name: "cid", age: 40.5}
	])`)

	cols, rows := run(t, s, `db.people.find({age: {$gt: 26}}).sort({age: -1})`)
	if len(rows) != 2 || cols[len(cols)-1].Name != DocumentColumn {
		t.Fatalf("find: cols %v rows %v", cols, rows)
	}
	if n := colIndex(cols, "name"); n < 0 || rows[0][n] != "cid" || rows[1][n] != "ann" {
		t.Fatalf("find order: %v", rows)
	}
	doc, ok := rows[1][len(cols)-1].(driver.JSON)
	if !ok || !strings.Contains(string(doc), `"tags"`) {
		t.Fatalf("document column %T %v", rows[1][len(cols)-1], rows[1][len(cols)-1])
	}

	_, rows = run(t, s, `db.people.aggregate([{$group: {_id: null, total: {$sum: "$age"}}}])`)
	if len(rows) != 1 {
		t.Fatalf("aggregate: %v", rows)
	}
	_, rows = run(t, s, `db.people.countDocuments({})`)
	if len(rows) != 1 || rows[0][0] != int64(3) {
		t.Fatalf("count: %v", rows)
	}

	r, err := s.Execute(context.Background(), driver.Statement{Text: `db.people.updateMany({age: {$lt: 35}}, {$inc: {age: 1}})`})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	// Paging through a server cursor.
	var docs []string
	for i := range 250 {
		docs = append(docs, "{n: "+strconv.Itoa(i)+"}")
	}
	run(t, s, "db.many.insertMany(["+strings.Join(docs, ",")+"])")
	r, err = s.Execute(context.Background(), driver.Statement{Text: "db.many.find().batchSize(50)"})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for {
		page, err := r.Next(context.Background(), 60)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		total += len(page)
	}
	r.Close()
	if total != 250 {
		t.Fatalf("paged %d documents", total)
	}

	_, err = s.Execute(context.Background(), driver.Statement{Text: "db.people.find({$bogus: 1})"})
	qe, ok := errors.AsType[*driver.QueryError](err)
	if !ok {
		t.Fatalf("server error %v", err)
	}
	_, err = s.Execute(context.Background(), driver.Statement{Text: "db.people.find({a: })"})
	if qe, ok = errors.AsType[*driver.QueryError](err); !ok || qe.Position < 0 {
		t.Fatalf("parse error %v", err)
	}
}

func TestITUseAndShow(t *testing.T) {
	_, s := itSetup(t)
	run(t, s, "db.a.insertOne({x: 1})")
	_, rows := run(t, s, "show collections")
	if len(rows) != 1 {
		t.Fatalf("show collections: %v", rows)
	}
	run(t, s, "use admin")
	if s.(*session).Database() != "admin" {
		t.Fatal("use did not switch")
	}
}

func TestITTree(t *testing.T) {
	c, s := itSetup(t)
	run(t, s, "db.items.insertOne({sku: 'a'})")
	run(t, s, "db.items.createIndex({sku: 1}, {unique: true})")
	run(t, s, `db.createView("big", "items", [{$match: {}}])`)
	ctx := context.Background()

	roots, err := c.Children(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var dbNode *driver.Node
	for _, n := range roots {
		if n.Name == itDB {
			dbNode = n
		}
	}
	if dbNode == nil {
		t.Fatalf("roots %v", roots)
	}
	colls, err := c.Children(ctx, dbNode)
	if err != nil {
		t.Fatal(err)
	}
	var items, view *driver.Node
	for _, n := range colls {
		switch n.Name {
		case "items":
			items = n
		case "big":
			view = n
		}
	}
	if items == nil || items.Kind != driver.NodeCollection || view == nil || view.Kind != driver.NodeView {
		t.Fatalf("collections %v", colls)
	}
	idx, err := c.Children(ctx, items)
	if err != nil || len(idx) != 2 {
		t.Fatalf("indexes %v %v", idx, err)
	}

	objs, err := c.(driver.Catalog).Objects(ctx, driver.Scope{Database: itDB})
	if err != nil || len(objs) < 2 {
		t.Fatalf("objects %v %v", objs, err)
	}
	det, err := c.(driver.Describer).Describe(ctx, items)
	if err != nil || len(det.Indexes) != 2 {
		t.Fatalf("describe %+v %v", det, err)
	}
}

func TestITEdit(t *testing.T) {
	c, s := itSetup(t)
	run(t, s, `db.e.insertMany([{_id: 1, a: "x"}, {_id: 2, a: "y"}])`)
	node := &driver.Node{Path: []string{itDB, "e"}, Kind: driver.NodeCollection, Name: "e"}
	ctx := context.Background()

	q, err := c.(driver.DataViewer).DataQuery(node, `{a: "x"}`, []string{"a DESC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, rows := run(t, s, q); len(rows) != 1 {
		t.Fatalf("data query %q: %v", q, rows)
	}

	ed := c.(driver.Editable)
	key, err := ed.RowKey(ctx, node)
	if err != nil || len(key) != 1 || key[0] != "_id" {
		t.Fatalf("row key %v %v", key, err)
	}
	err = ed.Apply(ctx, node, []driver.RowChange{
		{Op: driver.OpUpdate, Key: map[string]any{"_id": int64(1)}, Values: map[string]any{"a": "changed"}},
		{Op: driver.OpDelete, Key: map[string]any{"_id": int64(2)}},
		{Op: driver.OpInsert, Values: map[string]any{"_id": int64(3), "a": "new"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cols, rows := run(t, s, "db.e.find().sort({_id: 1})")
	a := colIndex(cols, "a")
	if len(rows) != 2 || rows[0][a] != "changed" || rows[1][a] != "new" {
		t.Fatalf("after apply %v", rows)
	}

	// Keys typed as text (as the grid sends them) find numeric ids.
	if err := ed.Apply(ctx, node, []driver.RowChange{{Op: driver.OpUpdate, Key: map[string]any{"_id": "3"}, Values: map[string]any{"a": "by text"}}}); err != nil {
		t.Fatal(err)
	}
	if _, rows := run(t, s, "db.e.find({_id: 3})"); rows[0][a] != "by text" {
		t.Fatalf("text key: %v", rows)
	}

	// A change to a row that no longer exists rolls everything back.
	err = ed.Apply(ctx, node, []driver.RowChange{
		{Op: driver.OpUpdate, Key: map[string]any{"_id": int64(1)}, Values: map[string]any{"a": "lost"}},
		{Op: driver.OpDelete, Key: map[string]any{"_id": int64(99)}},
	})
	if err == nil {
		t.Fatal("apply of a missing row succeeded")
	}
	if _, rows := run(t, s, "db.e.find({_id: 1})"); rows[0][a] != "changed" {
		t.Fatalf("partial apply kept: %v", rows)
	}
}

func TestITTransactions(t *testing.T) {
	_, s := itSetup(t)
	run(t, s, "db.createCollection('t')")
	tx := s.(driver.Transactional)
	ctx := context.Background()
	if err := tx.SetAutoCommit(ctx, false); err != nil {
		t.Fatal(err)
	}
	run(t, s, "db.t.insertOne({x: 1})")
	if !tx.InTx() {
		t.Fatal("no transaction after a write in manual mode")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, rows := run(t, s, "db.t.countDocuments({})"); rows[0][0] != int64(0) {
		t.Fatalf("rollback kept the insert: %v", rows)
	}
	run(t, s, "db.t.insertOne({x: 2})")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, rows := run(t, s, "db.t.countDocuments({})"); rows[0][0] != int64(1) {
		t.Fatalf("commit lost the insert: %v", rows)
	}
}

func TestITReadOnly(t *testing.T) {
	itSetup(t)
	s := itSession(t, itOpen(t, true))
	if _, rows := run(t, s, "db.x.find()"); len(rows) != 0 {
		t.Fatal(rows)
	}
	_, err := s.Execute(context.Background(), driver.Statement{Text: "db.x.insertOne({a: 1})"})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("insert on read-only: %v", err)
	}
}

func TestITCancel(t *testing.T) {
	_, s := itSetup(t)
	run(t, s, "db.c.insertOne({x: 1})")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Execute(ctx, driver.Statement{Text: `db.c.find({$where: "sleep(5000) || true"})`})
	if err == nil {
		t.Fatal("cancelled find succeeded")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("cancel took %v", d)
	}
	if _, rows := run(t, s, "db.c.countDocuments({})"); rows[0][0] != int64(1) {
		t.Fatalf("session unusable after cancel: %v", rows)
	}
}

func TestITExplain(t *testing.T) {
	_, s := itSetup(t)
	run(t, s, "db.p.insertMany([{a: 1}, {a: 2}])")
	plan, err := s.(driver.Explainer).Explain(context.Background(), driver.Statement{Text: "db.p.find({a: 1})"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Op == "" || plan.ActRows < 0 {
		t.Fatalf("plan %+v", plan)
	}
}

func TestITAuth(t *testing.T) {
	cfg := itConfig(t, false)
	cfg.User = "nobody"
	_, err := New().Open(context.Background(), cfg, "wrong")
	if !errors.Is(err, driver.ErrAuth) {
		t.Fatalf("bad credentials: %v", err)
	}
}
