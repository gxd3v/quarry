package redis

import (
	"errors"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestDataQuery(t *testing.T) {
	c := testConn(t, nil)
	key := func(name, detail string) *driver.Node {
		return &driver.Node{Path: KeyPath(0, name, ""), Kind: driver.NodeKey, Name: name, Detail: detail, Leaf: true}
	}
	tests := []struct {
		node    *driver.Node
		filter  string
		orderBy []string
		want    string
	}{
		{key("s", "string · ttl 5s"), "", nil, "GET s"},
		{key("h", "hash"), "", nil, "HGETALL h"},
		{key("h", "hash"), "f*", nil, "HSCAN h 0 MATCH f* COUNT 1000"},
		{key("l", "list"), "", nil, "LRANGE l 0 -1"},
		{key("l", "list"), "10", nil, "LRANGE l 10 -1"},
		{key("l", "list"), " 10 19 ", nil, "LRANGE l 10 19"},
		{key("my set", "set"), "", nil, `SMEMBERS "my set"`},
		{key("st", "set"), "a?", nil, "SSCAN st 0 MATCH a? COUNT 1000"},
		{key("z", "zset"), "", nil, "ZRANGE z 0 -1 WITHSCORES"},
		{key("z", "zset"), "", []string{"score DESC"}, "ZREVRANGE z 0 -1 WITHSCORES"},
		{key("z", "zset"), "m*", nil, "ZSCAN z 0 MATCH m* COUNT 1000"},
		{key("x", "stream"), "", nil, "XRANGE x - + COUNT 1000"},
		{key("x", "stream"), "1700-0", []string{"id desc"}, "XREVRANGE x + 1700-0 COUNT 1000"},
		{key("j", "json"), "", nil, "JSON.GET j"},
		{key("j", "json"), "$.a", nil, "JSON.GET j $.a"},
		{key("b\xff", "string"), "", nil, `GET "b\xff"`},
		{&driver.Node{Path: []string{"0"}, Kind: driver.NodeDatabase}, "", nil, "SCAN 0 MATCH * COUNT 1000"},
		{&driver.Node{Path: []string{"0", "user", "a*b"}, Kind: driver.NodeFolder}, "", nil, `SCAN 0 MATCH "user:a\\*b:*" COUNT 1000`},
		{&driver.Node{Path: []string{"0", "user"}, Kind: driver.NodeFolder}, "1*", nil, "SCAN 0 MATCH user:1* COUNT 1000"},
		{&driver.Node{Path: []string{"0", "user"}, Kind: driver.NodeFolder}, "user:1*", nil, "SCAN 0 MATCH user:1* COUNT 1000"},
	}
	for _, tt := range tests {
		got, err := c.DataQuery(tt.node, tt.filter, tt.orderBy)
		if err != nil || got != tt.want {
			t.Errorf("DataQuery(%q, %q) = %q, %v; want %q", tt.node.Path, tt.filter, got, err, tt.want)
		}
		if _, err := Fields(got); err != nil {
			t.Errorf("%q does not parse: %v", got, err)
		}
	}
	if _, err := c.DataQuery(key("l", "list"), "a b", nil); err == nil {
		t.Error("bad list filter accepted")
	}
	if _, err := dataQuery("MBbloom--", "b", "", nil); err == nil {
		t.Error("module type accepted")
	}
	if _, err := dataQuery("none", "b", "", nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing key: %v", err)
	}
}

func TestArgText(t *testing.T) {
	ok := map[any]string{
		"s": "s", int64(-3): "-3", 1.5: "1.5", true: "true", driver.Number("1.10"): "1.10",
		driver.JSON(`{"a":1}`): `{"a":1}`,
	}
	for v, want := range ok {
		if got, err := argText(v, "c"); err != nil || got != want {
			t.Errorf("argText(%#v) = %q, %v", v, got, err)
		}
	}
	if got, _ := argText([]byte("b"), "c"); got != "b" {
		t.Error("bytes")
	}
	if _, err := argText(nil, "value"); err == nil || !strings.Contains(err.Error(), "NULL") {
		t.Errorf("nil: %v", err)
	}
	if _, err := argText(struct{}{}, "value"); err == nil {
		t.Error("struct accepted")
	}
}

func TestChangeValidation(t *testing.T) {
	if err := knownColumns("hash", driver.RowChange{Key: map[string]any{"field": "a"}, Values: map[string]any{"bogus": 1}}); err == nil {
		t.Error("unknown column accepted")
	}
	if err := knownColumns("zset", driver.RowChange{Key: map[string]any{"member": "a"}, Values: map[string]any{"score": 1}}); err != nil {
		t.Error(err)
	}
	if err := knownColumns("stream", driver.RowChange{Values: map[string]any{"anything": "x"}}); err != nil {
		t.Error(err)
	}
	if n, err := index("12"); err != nil || n != 12 {
		t.Errorf("index = %d, %v", n, err)
	}
	if _, err := index("x"); err == nil {
		t.Error("bad index accepted")
	}
	for _, s := range []string{"1", "-2.5", "inf", "-inf", "1e3"} {
		if checkScore(s) != nil {
			t.Errorf("score %q rejected", s)
		}
	}
	if checkScore("abc") == nil {
		t.Error("bad score accepted")
	}
	if _, ok := errors.AsType[changeError](bad("x")); !ok {
		t.Error("bad must build a changeError")
	}
}

func TestReadOnlyConn(t *testing.T) {
	c, err := newConn(driver.Config{Host: "localhost", Port: 1, ReadOnly: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	node := &driver.Node{Path: KeyPath(0, "k", ""), Kind: driver.NodeKey}
	if err := c.Apply(t.Context(), node, []driver.RowChange{{Op: driver.OpDelete}}); !errors.Is(err, errReadOnly) {
		t.Errorf("Apply = %v", err)
	}
	if err := c.SetKeyTTL(t.Context(), node, 0); !errors.Is(err, errReadOnly) {
		t.Errorf("SetKeyTTL = %v", err)
	}
}
