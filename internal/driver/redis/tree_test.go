package redis

import (
	"reflect"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

func testConn(t *testing.T, opts map[string]string) *conn {
	t.Helper()
	c, err := newConn(driver.Config{Kind: driver.Redis, Host: "localhost", Port: 1, Options: opts}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestGroupKeys(t *testing.T) {
	keys := []string{"user:1", "user:2:name", "user:2:mail", "session", "cfg::x", "orders:", "a"}
	g := groupKeys(keys, "", ":")
	if want := map[string]int{"user": 3, "cfg": 1, "orders": 1}; !reflect.DeepEqual(g.folders, want) {
		t.Errorf("folders = %v, want %v", g.folders, want)
	}
	if want := []string{"a", "session"}; !reflect.DeepEqual(g.keys, want) {
		t.Errorf("keys = %v, want %v", g.keys, want)
	}
	g = groupKeys([]string{"user:1", "user:2:name", "user:2:mail"}, "user:", ":")
	if want := map[string]int{"2": 2}; !reflect.DeepEqual(g.folders, want) {
		t.Errorf("level 2 folders = %v", g.folders)
	}
	if want := []string{"user:1"}; !reflect.DeepEqual(g.keys, want) {
		t.Errorf("level 2 keys = %v", g.keys)
	}
	g = groupKeys([]string{"cfg::x"}, "cfg:", ":")
	if want := map[string]int{"": 1}; !reflect.DeepEqual(g.folders, want) {
		t.Errorf("empty segment folders = %v", g.folders)
	}
	g = groupKeys([]string{"a/b", "a/c/d", "x:y"}, "", "/")
	if want := map[string]int{"a": 2}; !reflect.DeepEqual(g.folders, want) || !reflect.DeepEqual(g.keys, []string{"x:y"}) {
		t.Errorf("custom separator: %v %v", g.folders, g.keys)
	}
}

func TestPaths(t *testing.T) {
	c := testConn(t, nil)
	p := KeyPath(2, "user:7:name", "")
	if want := []string{"2", "user", "7", keyMarker + "user:7:name"}; !reflect.DeepEqual(p, want) {
		t.Fatalf("KeyPath = %q", p)
	}
	tg, err := c.locate(&driver.Node{Path: p, Kind: driver.NodeKey})
	if err != nil || !tg.isKey || tg.key != "user:7:name" || tg.db != 2 {
		t.Errorf("locate key = %+v, %v", tg, err)
	}
	// A folder and a key with the same name have different paths.
	folder := []string{"0", "user"}
	if reflect.DeepEqual(folder, KeyPath(0, "user", "")) {
		t.Error("folder and key paths collide")
	}
	tg, _ = c.locate(&driver.Node{Path: folder, Kind: driver.NodeFolder})
	if tg.isKey || tg.prefix != "user:" {
		t.Errorf("locate folder = %+v", tg)
	}
	tg, _ = c.locate(&driver.Node{Path: []string{"3"}, Kind: driver.NodeDatabase})
	if tg.isKey || tg.prefix != "" || tg.db != 3 {
		t.Errorf("locate db = %+v", tg)
	}
	// Folder segments starting with NUL are escaped.
	odd := KeyPath(0, "\x00k:x:y", "")
	if odd[1] != folderEscape+"\x00k" {
		t.Errorf("escaped segment = %q", odd[1])
	}
	tg, _ = c.locate(&driver.Node{Path: odd[:2], Kind: driver.NodeFolder})
	if tg.isKey || tg.prefix != "\x00k:" {
		t.Errorf("locate escaped folder = %+v", tg)
	}
	// Binary names stay valid UTF-8 in paths and round-trip.
	bin := "b\xff:k\xfe"
	bp := KeyPath(0, bin, "")
	for _, e := range bp {
		if !utf8.ValidString(e) {
			t.Errorf("path element %q is not UTF-8", e)
		}
	}
	tg, err = c.locate(&driver.Node{Path: bp, Kind: driver.NodeKey})
	if err != nil || tg.key != bin {
		t.Errorf("locate binary key = %+v, %v", tg, err)
	}
	tg, err = c.locate(&driver.Node{Path: bp[:2], Kind: driver.NodeFolder})
	if err != nil || tg.prefix != "b\xff:" {
		t.Errorf("locate binary folder = %+v, %v", tg, err)
	}
	// Nodes built from Catalog objects.
	tg, err = c.locate(&driver.Node{Path: []string{"1"}, Kind: driver.NodeKey, Name: "plain"})
	if err != nil || !tg.isKey || tg.key != "plain" || tg.db != 1 {
		t.Errorf("locate object node = %+v, %v", tg, err)
	}
	if _, err := c.locate(nil); err == nil {
		t.Error("nil node accepted")
	}
	if _, err := c.locate(&driver.Node{Path: []string{"x"}}); err == nil {
		t.Error("bad database accepted")
	}
}

func TestGlobEscape(t *testing.T) {
	if got := globEscape(`a*b?[c]\`); got != `a\*b\?\[c\]\\` {
		t.Errorf("globEscape = %s", got)
	}
}

func TestKeyspace(t *testing.T) {
	info := "# Keyspace\r\ndb0:keys=3,expires=1,avg_ttl=0\r\ndb12:keys=1500,expires=0,avg_ttl=0\r\n"
	if got := keyspace(info); !reflect.DeepEqual(got, map[int]int64{0: 3, 12: 1500}) {
		t.Errorf("keyspace = %v", got)
	}
}

func TestDetail(t *testing.T) {
	if got := (meta{typ: "hash", ttl: 3600}).detail(); got != "hash · ttl 3600s" {
		t.Errorf("detail = %q", got)
	}
	if got := (meta{typ: "ReJSON-RL", ttl: -1}).detail(); got != "json" {
		t.Errorf("detail = %q", got)
	}
	if detailType("hash · ttl 3600s") != "hash" || detailType("zset") != "zset" || detailType("") != "" {
		t.Error("detailType")
	}
}

func TestAddresses(t *testing.T) {
	tests := []struct {
		host string
		port int
		want []string
	}{
		{"", 6379, []string{"localhost:6379"}},
		{"redis.local", 7000, []string{"redis.local:7000"}},
		{"a:7001, b ,[::1]:7003", 7000, []string{"a:7001", "b:7000", "[::1]:7003"}},
		{"::1", 6379, []string{"[::1]:6379"}},
	}
	for _, tt := range tests {
		got, err := addresses(tt.host, tt.port)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("addresses(%q) = %q, %v", tt.host, got, err)
		}
	}
	if _, err := addresses("a:99999", 1); err == nil {
		t.Error("bad port accepted")
	}
}

func TestNewConnOptions(t *testing.T) {
	c := testConn(t, map[string]string{"cluster": "true"})
	if !c.cluster || !c.opts.IsClusterMode {
		t.Error("cluster option ignored")
	}
	c = testConn(t, map[string]string{"cluster": "n2:7001,n3"})
	if !c.cluster || !reflect.DeepEqual(c.opts.Addrs, []string{"localhost:1", "n2:7001", "n3:1"}) {
		t.Errorf("cluster seeds = %q", c.opts.Addrs)
	}
	s, err := newConn(driver.Config{Host: "a,b", Options: map[string]string{"sentinel_master": "mymaster", "tls": "true", "sentinel_auth": "true"}}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.cluster || s.opts.MasterName != "mymaster" || s.opts.Addrs[0] != "a:26379" || s.tls == nil ||
		s.opts.Password != "pw" || s.opts.SentinelPassword != "pw" {
		t.Errorf("sentinel options = %+v", s.opts)
	}
	c2, err := newConn(driver.Config{Host: "a,b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if !c2.cluster {
		t.Error("several hosts should mean Cluster")
	}
	if _, err := newConn(driver.Config{Host: "a,b", Options: map[string]string{"cluster": "false"}}, ""); err == nil {
		t.Error("several hosts with cluster=false accepted")
	}
	if _, err := newConn(driver.Config{Database: "x"}, ""); err == nil {
		t.Error("bad database accepted")
	}
	if _, err := newConn(driver.Config{Database: "3", Options: map[string]string{"cluster": "1"}}, ""); err == nil {
		t.Error("cluster with db 3 accepted")
	}
	if _, err := newConn(driver.Config{Options: map[string]string{"connect_timeout": "soon"}}, ""); err == nil {
		t.Error("bad timeout accepted")
	}
	c3 := testConn(t, map[string]string{"separator": "/", "username": "ann", "connect_timeout": "3"})
	if c3.sep != "/" || c3.opts.Username != "ann" || c3.opts.DialTimeout.Seconds() != 3 {
		t.Errorf("options = sep %q user %q timeout %v", c3.sep, c3.opts.Username, c3.opts.DialTimeout)
	}
	u, err := newConn(driver.Config{User: "bob", Options: map[string]string{"username": "ann"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if u.opts.Username != "bob" {
		t.Errorf("Config.User should win, got %q", u.opts.Username)
	}
	if slices.Contains([]string{c3.where()}, "") {
		t.Error("where")
	}
}
