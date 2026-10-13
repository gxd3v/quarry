package connection

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/postgres"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/service"
)

// itConfig reads QUARRY_TEST_PG_DSN, e.g.
// postgres://postgres:quarry@localhost:55432/postgres?sslmode=disable
func itConfig(t *testing.T) (*quarryv1.ConnectionConfig, string) {
	t.Helper()
	dsn := os.Getenv("QUARRY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_PG_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	cfg := &quarryv1.ConnectionConfig{
		Name:     "it",
		Kind:     string(driver.Postgres),
		Host:     u.Hostname(),
		Port:     int32(port),
		User:     u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"),
		Options:  map[string]string{},
	}
	for k, v := range u.Query() {
		cfg.Options[k] = v[0]
	}
	return cfg, pw
}

func TestITPostgres(t *testing.T) {
	cfg, pw := itConfig(t)
	m, err := New(filepath.Join(t.TempDir(), "connections.toml"), store.NewMemory(), WithLookup(lookupOf(postgres.New())))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	svc := NewService(m)
	ctx := t.Context()

	wrong := "not-the-password-x7"
	_, err = svc.TestConnection(ctx, connect.NewRequest(&quarryv1.TestConnectionRequest{Config: cfg, Password: &wrong}))
	wantCode(t, err, connect.CodeUnauthenticated)
	if strings.Contains(err.Error(), wrong) {
		t.Errorf("password leaked: %v", err)
	}
	tested, err := svc.TestConnection(ctx, connect.NewRequest(&quarryv1.TestConnectionRequest{Config: cfg, Password: &pw}))
	if err != nil || tested.Msg.LatencyMs < 1 {
		t.Fatalf("test = %v, %v", tested, err)
	}

	saved, err := svc.SaveConnection(ctx, connect.NewRequest(&quarryv1.SaveConnectionRequest{Config: cfg, Password: &pw}))
	if err != nil {
		t.Fatal(err)
	}
	id := saved.Msg.Connection.Config.Id
	res, err := svc.Connect(ctx, connect.NewRequest(&quarryv1.ConnectRequest{Id: id}))
	if err != nil || res.Msg.Status.State != stConnected || res.Msg.Status.LatencyMs < 1 {
		t.Fatalf("connect = %v, %v", res, err)
	}

	conn, live, err := m.Live(id)
	if err != nil || live.Database != cfg.Database {
		t.Fatalf("Live = %+v, %v", live, err)
	}
	dbs, err := conn.Children(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(dbs, func(n *driver.Node) bool { return n.Name == cfg.Database }) {
		t.Errorf("databases %v lack %q", dbs, cfg.Database)
	}

	if _, err := svc.Disconnect(ctx, connect.NewRequest(&quarryv1.DisconnectRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Live(id); !errors.Is(err, service.ErrNotConnected) {
		t.Errorf("Live after disconnect: %v", err)
	}
	if _, err := conn.Children(ctx, nil); err == nil {
		t.Error("the connection still works after disconnect")
	}
}
