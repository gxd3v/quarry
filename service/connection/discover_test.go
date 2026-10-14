package connection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
)

func TestDiscoverConnections(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("PGPASSFILE", write("pgpass", "db.prod:5432:app:svc:prodpw\nlocalhost:5432:dev:me:devpw\n"))
	t.Setenv("PGSERVICEFILE", write("services.conf", "[prod]\nhost=db.prod\ndbname=app\nuser=svc\n[saved]\nhost=already\ndbname=x\nuser=u\n"))
	t.Setenv("PGSYSCONFDIR", "")
	t.Setenv("TNS_ADMIN", dir)
	t.Setenv("ORACLE_HOME", "")
	write("tnsnames.ora", "prod = (DESCRIPTION = (ADDRESS = (HOST = ora)(PORT = 1521))(CONNECT_DATA = (SERVICE_NAME = pdb)))\n")
	bad := write("broken_pg_service.conf", "no section=here\n")

	m, _, _ := newTestManager(t, nil)
	client := newClient(t, m)
	if _, err := client.SaveConnection(t.Context(), connect.NewRequest(&quarryv1.SaveConnectionRequest{
		Config: &quarryv1.ConnectionConfig{Name: "prod", Kind: "postgres", Host: "already", Port: 5432, Database: "x", User: "u"},
	})); err != nil {
		t.Fatal(err)
	}

	res, err := client.DiscoverConnections(t.Context(), connect.NewRequest(&quarryv1.DiscoverConnectionsRequest{
		Paths: []string{bad, filepath.Join(dir, "missing.ora")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Errors) != 1 || !strings.HasPrefix(res.Msg.Errors[0], bad+":") {
		t.Errorf("errors %q", res.Msg.Errors)
	}
	got := map[string]*quarryv1.DiscoveredConnection{}
	for _, c := range res.Msg.Connections {
		got[c.Config.Name] = c
	}
	// The pgpass line for db.prod duplicates the service and is dropped; the
	// service gets its password. "prod" is taken by the saved connection.
	if len(got) != 3 {
		t.Fatalf("discovered %v", res.Msg.Connections)
	}
	svc := got["prod (2)"]
	if svc == nil || svc.Config.Kind != "postgres" || svc.GetPassword() != "prodpw" || svc.Config.Env != "dev" || svc.Source != os.Getenv("PGSERVICEFILE") {
		t.Errorf("service %v", svc)
	}
	dev := got["localhost/dev"]
	if dev == nil || dev.GetPassword() != "devpw" || dev.Config.Env != "local" {
		t.Errorf("pgpass %v", dev)
	}
	ora := got["prod (3)"]
	if ora == nil || ora.Config.Kind != "oracle" || ora.Config.Database != "pdb" || ora.Password != nil || ora.Config.Id != "" {
		t.Errorf("tns %v", ora)
	}
}
