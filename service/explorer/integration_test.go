package explorer

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/postgres"
)

// TestITPostgres browses a throwaway schema through a real connection. It
// needs QUARRY_TEST_PG_DSN, e.g.
// postgres://postgres:quarry@localhost:55432/postgres?sslmode=disable
func TestITPostgres(t *testing.T) {
	dsn := os.Getenv("QUARRY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_PG_DSN not set")
	}
	ctx := t.Context()
	schema := "quarry_explorer_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	_, err = admin.Exec(ctx, fmt.Sprintf(`
CREATE SCHEMA %[1]s;
CREATE TABLE %[1]s.items (
	id int PRIMARY KEY,
	name text NOT NULL DEFAULT 'x'
);
CREATE INDEX items_name_idx ON %[1]s.items (name);
CREATE VIEW %[1]s.named AS SELECT name FROM %[1]s.items;
COMMENT ON COLUMN %[1]s.items.name IS 'display name';`, schema))
	if err != nil {
		t.Fatal(err)
	}

	cfg, pw := itConfig(t, dsn)
	conn, err := postgres.New().Open(ctx, cfg, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := newClient(t, fakeLive{"pg": conn})

	children := func(parent *quarryv1.Node) []*quarryv1.Node {
		t.Helper()
		res, err := client.Children(ctx, connect.NewRequest(&quarryv1.ChildrenRequest{ConnId: "pg", Parent: parent}))
		if err != nil {
			t.Fatalf("Children(%v): %v", parent, err)
		}
		return res.Msg.Nodes
	}
	find := func(nodes []*quarryv1.Node, name string) *quarryv1.Node {
		t.Helper()
		i := slices.IndexFunc(nodes, func(n *quarryv1.Node) bool { return n.Name == name })
		if i < 0 {
			t.Fatalf("%q not among %v", name, nodes)
		}
		return nodes[i]
	}

	db := find(children(nil), cfg.Database)
	sc := find(children(db), schema)
	tables := find(children(sc), "Tables")
	table := find(children(tables), "items")
	if table.Kind != "table" || !slices.Equal(table.Path, []string{cfg.Database, schema, "tables", "items"}) {
		t.Fatalf("table node %v", table)
	}

	cat, err := client.Catalog(ctx, connect.NewRequest(&quarryv1.CatalogRequest{ConnId: "pg", Scope: &quarryv1.Scope{Database: cfg.Database}}))
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, o := range cat.Msg.Objects {
		if o.Schema == schema {
			mine = append(mine, o.Kind+":"+o.Name+":"+strconv.Itoa(len(o.Columns)))
		}
	}
	if want := []string{"view:named:1", "table:items:2"}; !slices.Equal(slices.Sorted(slices.Values(mine)), slices.Sorted(slices.Values(want))) {
		t.Errorf("catalog objects %v, want %v", mine, want)
	}

	desc, err := client.Describe(ctx, connect.NewRequest(&quarryv1.DescribeRequest{ConnId: "pg", Node: table}))
	if err != nil {
		t.Fatal(err)
	}
	d := desc.Msg
	if len(d.Columns) != 2 || !d.Columns[0].PrimaryKey || d.Columns[1].Comment != "display name" || d.Columns[1].Nullable {
		t.Errorf("columns %v", d.Columns)
	}
	if !slices.ContainsFunc(d.Indexes, func(r *quarryv1.DetailRow) bool { return r.Name == "items_name_idx" }) {
		t.Errorf("indexes %v", d.Indexes)
	}
	if !strings.Contains(d.Ddl, "CREATE TABLE") || !strings.Contains(d.Ddl, "items") {
		t.Errorf("ddl %q", d.Ddl)
	}

	_, err = client.Describe(ctx, connect.NewRequest(&quarryv1.DescribeRequest{ConnId: "pg", Node: sc}))
	if err == nil {
		t.Error("describing a schema succeeded")
	}
}

func itConfig(t *testing.T, dsn string) (driver.Config, string) {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	cfg := driver.Config{
		Kind:     driver.Postgres,
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"),
		Options:  map[string]string{},
	}
	for k, v := range u.Query() {
		cfg.Options[k] = v[0]
	}
	return cfg, pw
}
