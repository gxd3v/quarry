package store

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

func sampleConfigs() []driver.Config {
	return []driver.Config{
		{
			ID: uuid.NewV7(), Name: "orders prod", Kind: driver.Postgres, Env: "prod",
			Host: "db.example.com", Port: 5432, User: "app", Database: "orders", ReadOnly: true,
			Options: map[string]string{"sslmode": "require", "application.name": "quarry"},
		},
		{ID: uuid.NewV7(), Name: "cache", Kind: driver.Redis, Host: "localhost", Database: "3"},
		{ID: uuid.NewV7(), Name: "legacy", Kind: driver.Oracle, Host: "ora", Port: 1521, User: "scott",
			Options: map[string]string{"service": "ORCLPDB1"}},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "connections.toml")
	want := sampleConfigs()
	if err := SaveConnections(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	got, err := LoadConnections(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want empty non-nil slice", got)
	}
}

func TestLoadAssignsIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.toml")
	dup := uuid.NewV7()
	src := "[[connection]]\nname = 'a'\nkind = 'postgres'\n\n" +
		"[[connection]]\nid = '" + dup.String() + "'\nname = 'b'\n\n" +
		"[[connection]]\nid = '" + dup.String() + "'\nname = 'c'\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d connections, want 3", len(got))
	}
	if got[0].ID == uuid.Nil() {
		t.Error("missing ID not assigned")
	}
	if got[1].ID != dup {
		t.Errorf("first duplicate ID changed: %v", got[1].ID)
	}
	if got[2].ID == dup || got[2].ID == uuid.Nil() {
		t.Errorf("second duplicate ID not replaced: %v", got[2].ID)
	}
}

func TestLoadInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.toml")
	if err := os.WriteFile(path, []byte("[[connection]]\nid = 'zz'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConnections(path)
	if err == nil || !strings.Contains(err.Error(), path+":2:") {
		t.Fatalf("got %v, want error with position", err)
	}
}

func TestSaveAssignsMissingIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.toml")
	cfgs := []driver.Config{{Name: "x", Kind: driver.Redis}}
	if err := SaveConnections(path, cfgs); err != nil {
		t.Fatal(err)
	}
	if cfgs[0].ID == uuid.Nil() {
		t.Fatal("ID not assigned")
	}
	got, err := LoadConnections(path)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != cfgs[0].ID {
		t.Fatalf("got ID %v, want %v", got[0].ID, cfgs[0].ID)
	}
}

func TestSaveEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.toml")
	if err := SaveConnections(path, sampleConfigs()); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnections(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no connections", got, err)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.toml")
	for range 3 {
		if err := SaveConnections(path, sampleConfigs()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "connections.toml" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir contains %v, want only connections.toml", names)
	}
}

func TestSaveFailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.toml")
	if err := SaveConnections(path, sampleConfigs()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	// Renaming a file over a non-empty directory fails on every OS.
	target := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(target, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnections(target, sampleConfigs()); err == nil {
		t.Fatal("save over a directory succeeded")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing file changed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (temp file left behind?)", len(entries))
	}
}

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "quarry")
	path := filepath.Join(dir, "connections.toml")
	if err := SaveConnections(path, sampleConfigs()); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", p, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	existing := driver.Config{ID: uuid.NewV7(), Name: "Orders", Kind: driver.Postgres}
	others := []driver.Config{existing}
	ok := driver.Config{ID: uuid.NewV7(), Name: "new", Kind: driver.MongoDB}
	with := func(f func(*driver.Config)) driver.Config {
		c := ok
		f(&c)
		return c
	}
	tests := []struct {
		name string
		cfg  driver.Config
		err  string
	}{
		{"valid", ok, ""},
		{"empty env", with(func(c *driver.Config) { c.Env = "" }), ""},
		{"prod env", with(func(c *driver.Config) { c.Env = "prod" }), ""},
		{"port max", with(func(c *driver.Config) { c.Port = 65535 }), ""},
		{"same entry keeps its name", with(func(c *driver.Config) { c.ID = existing.ID; c.Name = "orders" }), ""},
		{"no name", with(func(c *driver.Config) { c.Name = "  " }), "Name is required"},
		{"duplicate name", with(func(c *driver.Config) { c.Name = " ORDERS " }), "already exists"},
		{"same name in another folder", with(func(c *driver.Config) { c.Name = "orders"; c.Group = "Acme/PRD" }), ""},
		{"no kind", with(func(c *driver.Config) { c.Kind = "" }), "type is required"},
		{"bad kind", with(func(c *driver.Config) { c.Kind = "mysql" }), "Unknown database type"},
		{"negative port", with(func(c *driver.Config) { c.Port = -1 }), "Port"},
		{"port too big", with(func(c *driver.Config) { c.Port = 65536 }), "Port"},
		{"bad env", with(func(c *driver.Config) { c.Env = "staging" }), "Environment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.cfg, others)
			switch {
			case tt.err == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
				t.Fatalf("got %v, want error containing %q", err, tt.err)
			}
		})
	}
}

func TestGroupPath(t *testing.T) {
	for in, want := range map[string]string{"": "", " A / PRD/ ": "A/PRD", "A//B": "A/B", "/": "", "Acme": "Acme"} {
		if got := GroupPath(in); got != want {
			t.Errorf("GroupPath(%q) = %q, want %q", in, got, want)
		}
	}
	others := []driver.Config{{ID: uuid.NewV7(), Name: "postgres", Kind: driver.Postgres, Group: "Acme/PRD"}}
	err := Validate(driver.Config{ID: uuid.NewV7(), Name: "Postgres", Kind: driver.Postgres, Group: " acme / prd "}, others)
	if err == nil || !strings.Contains(err.Error(), "in acme/prd") {
		t.Fatalf("duplicate in the same folder: %v", err)
	}
}

func TestDefaultPort(t *testing.T) {
	for k, want := range map[driver.Kind]int{
		driver.Postgres: 5432, driver.Oracle: 1521, driver.MongoDB: 27017, driver.Redis: 6379, "x": 0,
	} {
		if got := DefaultPort(k); got != want {
			t.Errorf("DefaultPort(%q) = %d, want %d", k, got, want)
		}
	}
}

func TestPaths(t *testing.T) {
	cfg, err := ConfigDir()
	if err != nil {
		t.Skip("no config dir:", err)
	}
	if filepath.Base(cfg) != "quarry" {
		t.Errorf("ConfigDir = %q", cfg)
	}
	f, err := ConnectionsFile()
	if err != nil || f != filepath.Join(cfg, "connections.toml") {
		t.Errorf("ConnectionsFile = %q, %v", f, err)
	}
	if st, err := StateDir(); err != nil || !strings.Contains(st, "quarry") {
		t.Errorf("StateDir = %q, %v", st, err)
	}
}

func TestStateDirXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG only")
	}
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if got, _ := StateDir(); got != filepath.Join(dir, "quarry") {
		t.Errorf("StateDir = %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", dir)
	if got, _ := StateDir(); got != filepath.Join(dir, ".local", "state", "quarry") {
		t.Errorf("StateDir = %q", got)
	}
}
