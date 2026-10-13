package explorer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
	"github.com/gxd3v/quarry/service/catalog"
)

type fakeLive map[string]driver.Conn

func (l fakeLive) Live(id string) (driver.Conn, driver.Config, error) {
	switch id {
	case "closed":
		return nil, driver.Config{Kind: driver.Postgres}, fmt.Errorf("closed: %w", service.ErrNotConnected)
	}
	c, ok := l[id]
	if !ok {
		return nil, driver.Config{}, connect.NewError(connect.CodeNotFound, errors.New("no such connection"))
	}
	return c, driver.Config{Kind: driver.Redis}, nil
}

func (l fakeLive) Config(id string) (driver.Config, bool) {
	_, ok := l[id]
	return driver.Config{}, ok
}

// plainConn only browses.
type plainConn struct {
	parents []*driver.Node
	err     error
}

func (c *plainConn) Children(_ context.Context, parent *driver.Node) ([]*driver.Node, error) {
	c.parents = append(c.parents, parent)
	if c.err != nil {
		return nil, c.err
	}
	if parent == nil {
		return []*driver.Node{{Path: []string{"app"}, Kind: driver.NodeDatabase, Name: "app"}}, nil
	}
	return []*driver.Node{{Path: append(parent.Path, "t"), Kind: driver.NodeTable, Name: "t", Detail: "~3", Leaf: true}}, nil
}

func (c *plainConn) NewSession(context.Context, driver.Scope) (driver.Session, error) {
	return nil, errors.New("no sessions")
}

func (c *plainConn) Close() error { return nil }

// richConn adds a catalog and object details.
type richConn struct {
	plainConn
	scope  driver.Scope
	loads  int
	target *driver.Node
}

func (c *richConn) Objects(_ context.Context, scope driver.Scope) ([]driver.Object, error) {
	c.scope = scope
	c.loads++
	return []driver.Object{
		{Schema: "public", Name: "users", Kind: driver.NodeTable, Columns: []driver.Column{{Name: "id", Type: "int4"}, {Name: "name", Type: "text", Nullable: true}}},
		{Schema: "public", Name: "add", Kind: driver.NodeFunction, Detail: "(int, int)"},
	}, nil
}

func (c *richConn) Describe(_ context.Context, target *driver.Node) (*driver.ObjectDetails, error) {
	c.target = target
	if target.Name == "missing" {
		return nil, errors.New("public.missing does not exist")
	}
	return &driver.ObjectDetails{
		Columns:     []driver.ColumnInfo{{Name: "id", Type: "int4", PrimaryKey: true}, {Name: "name", Type: "text", Default: "'x'", Comment: "c", Nullable: true}},
		Indexes:     []driver.DetailRow{{Name: "users_pkey", Definition: "UNIQUE btree (id)"}},
		Constraints: []driver.DetailRow{{Name: "users_pkey", Definition: "PRIMARY KEY (id)"}},
		Stats:       []driver.DetailRow{{Name: "Estimated rows", Definition: "3"}},
		DDL:         "CREATE TABLE users ();",
	}, nil
}

func newClient(t *testing.T, live service.Live) quarryv1connect.ExplorerServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewExplorerServiceHandler(New(live, catalog.New(live))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return quarryv1connect.NewExplorerServiceClient(srv.Client(), srv.URL)
}

func TestChildren(t *testing.T) {
	plain := &plainConn{}
	client := newClient(t, fakeLive{"plain": plain, "broken": &plainConn{err: errors.New("connection lost: EOF")}})
	ctx := t.Context()

	roots, err := client.Children(ctx, connect.NewRequest(&quarryv1.ChildrenRequest{ConnId: "plain"}))
	if err != nil {
		t.Fatal(err)
	}
	if n := roots.Msg.Nodes; len(n) != 1 || n[0].Kind != "database" || !reflect.DeepEqual(n[0].Path, []string{"app"}) {
		t.Fatalf("roots %v", n)
	}
	kids, err := client.Children(ctx, connect.NewRequest(&quarryv1.ChildrenRequest{ConnId: "plain", Parent: roots.Msg.Nodes[0]}))
	if err != nil {
		t.Fatal(err)
	}
	if n := kids.Msg.Nodes; len(n) != 1 || n[0].Name != "t" || n[0].Detail != "~3" || !n[0].Leaf || !reflect.DeepEqual(n[0].Path, []string{"app", "t"}) {
		t.Fatalf("children %v", n)
	}
	if plain.parents[0] != nil || plain.parents[1].Kind != driver.NodeDatabase {
		t.Errorf("parents passed to the driver: %v", plain.parents)
	}

	for id, code := range map[string]connect.Code{
		"broken":  connect.CodeUnavailable,
		"closed":  connect.CodeFailedPrecondition,
		"unknown": connect.CodeNotFound,
	} {
		_, err := client.Children(ctx, connect.NewRequest(&quarryv1.ChildrenRequest{ConnId: id}))
		if connect.CodeOf(err) != code {
			t.Errorf("%s: %v, want %v", id, err, code)
		}
	}
}

func TestCatalog(t *testing.T) {
	rich := &richConn{}
	client := newClient(t, fakeLive{"rich": rich, "plain": &plainConn{}})
	ctx := t.Context()

	res, err := client.Catalog(ctx, connect.NewRequest(&quarryv1.CatalogRequest{ConnId: "rich", Scope: &quarryv1.Scope{Database: "app", Schema: "public"}}))
	if err != nil {
		t.Fatal(err)
	}
	if rich.scope != (driver.Scope{Database: "app", Schema: "public"}) {
		t.Errorf("scope %+v", rich.scope)
	}
	objs := res.Msg.Objects
	if len(objs) != 2 || objs[0].Name != "users" || objs[0].Kind != "table" || len(objs[0].Columns) != 2 || objs[0].Columns[1].Type != "text" ||
		objs[1].Kind != "function" || objs[1].Detail != "(int, int)" || len(objs[1].Columns) != 0 {
		t.Errorf("objects %v", objs)
	}

	req := &quarryv1.CatalogRequest{ConnId: "rich", Scope: &quarryv1.Scope{Database: "app", Schema: "public"}}
	if _, err := client.Catalog(ctx, connect.NewRequest(req)); err != nil || rich.loads != 1 {
		t.Errorf("cached catalog: %v, loads %d", err, rich.loads)
	}
	req.Refresh = true
	if _, err := client.Catalog(ctx, connect.NewRequest(req)); err != nil || rich.loads != 2 {
		t.Errorf("refreshed catalog: %v, loads %d", err, rich.loads)
	}

	for id, code := range map[string]connect.Code{
		"plain":   connect.CodeUnimplemented,
		"closed":  connect.CodeFailedPrecondition,
		"unknown": connect.CodeNotFound,
	} {
		_, err := client.Catalog(ctx, connect.NewRequest(&quarryv1.CatalogRequest{ConnId: id}))
		if connect.CodeOf(err) != code {
			t.Errorf("%s: %v, want %v", id, err, code)
		}
	}
}

func TestDescribe(t *testing.T) {
	rich := &richConn{}
	client := newClient(t, fakeLive{"rich": rich, "plain": &plainConn{}})
	ctx := t.Context()
	node := &quarryv1.Node{Path: []string{"app", "public", "tables", "users"}, Kind: "table", Name: "users"}

	res, err := client.Describe(ctx, connect.NewRequest(&quarryv1.DescribeRequest{ConnId: "rich", Node: node}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rich.target.Path, node.Path) {
		t.Errorf("target %+v", rich.target)
	}
	d := res.Msg
	if len(d.Columns) != 2 || !d.Columns[0].PrimaryKey || d.Columns[1].Default != "'x'" || d.Columns[1].Comment != "c" || !d.Columns[1].Nullable ||
		len(d.Indexes) != 1 || len(d.Constraints) != 1 || len(d.Triggers) != 0 || d.Stats[0].Definition != "3" || d.Ddl != "CREATE TABLE users ();" {
		t.Errorf("details %v", d)
	}

	for _, tc := range []struct {
		name string
		req  *quarryv1.DescribeRequest
		code connect.Code
	}{
		{"no node", &quarryv1.DescribeRequest{ConnId: "rich"}, connect.CodeInvalidArgument},
		{"missing", &quarryv1.DescribeRequest{ConnId: "rich", Node: &quarryv1.Node{Name: "missing"}}, connect.CodeUnavailable},
		{"unsupported", &quarryv1.DescribeRequest{ConnId: "plain", Node: node}, connect.CodeUnimplemented},
		{"closed", &quarryv1.DescribeRequest{ConnId: "closed", Node: node}, connect.CodeFailedPrecondition},
	} {
		_, err := client.Describe(ctx, connect.NewRequest(tc.req))
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.code)
		}
	}
}

func TestDriverErrorOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := connect.CodeOf(driverError(ctx, errors.New("x"))); got != connect.CodeCanceled {
		t.Errorf("code %v", got)
	}
}
