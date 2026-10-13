package redis

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

// TestITCluster runs against a Redis Cluster named by
// QUARRY_TEST_REDIS_CLUSTER, a comma-separated list of seed nodes such as
// 127.0.0.1:47001,127.0.0.1:47002 (no password).
func TestITCluster(t *testing.T) {
	seeds := os.Getenv("QUARRY_TEST_REDIS_CLUSTER")
	if seeds == "" {
		t.Skip("QUARRY_TEST_REDIS_CLUSTER not set")
	}
	p := "qit:" + strings.ToLower(rand.Text()[:8]) + ":"
	admin := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: strings.Split(seeds, ",")})
	defer admin.Close()
	t.Cleanup(func() {
		ctx := context.Background()
		admin.ForEachMaster(ctx, func(ctx context.Context, m *goredis.Client) error {
			iter := m.Scan(ctx, 0, globEscape(p)+"*", 1000).Iterator()
			for iter.Next(ctx) {
				m.Del(ctx, iter.Val())
			}
			return nil
		})
	})

	dc, err := New().Open(t.Context(), driver.Config{Kind: driver.Redis, Host: seeds}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	c := dc.(*conn)
	if !c.cluster {
		t.Fatal("several hosts should open a Cluster")
	}
	s := itSession(t, c, "")
	for i := range 300 {
		exec(t, s, "SET "+p+"k:"+strconv.Itoa(i)+" "+strconv.Itoa(i))
	}

	roots, err := c.Children(t.Context(), nil)
	if err != nil || len(roots) != 1 || roots[0].Name != "db0" {
		t.Fatalf("roots = %v, %v", roots, err)
	}
	segs := strings.Split(p+"k", ":")
	keys, err := c.Children(t.Context(), &driver.Node{Path: append([]string{"0"}, segs...), Kind: driver.NodeFolder})
	if err != nil || len(keys) != 300 {
		t.Errorf("folder children = %d, %v", len(keys), err)
	}

	// SCAN walks every primary.
	_, rows := exec(t, s, "SCAN 0 MATCH "+p+"k:* COUNT 20")
	if len(rows) != 300 || len(distinct(rows)) != 300 {
		t.Errorf("cluster SCAN = %d rows", len(rows))
	}

	// Emulated MULTI, keys in one slot.
	h := "{" + p + "}"
	exec(t, s, "MULTI")
	if v := value(t, s, "SET "+h+"a 1"); v != "QUEUED" {
		t.Errorf("queued = %#v", v)
	}
	exec(t, s, "INCR "+h+"a")
	if _, rows := exec(t, s, "EXEC"); len(rows) != 2 || rows[1][1] != int64(2) {
		t.Errorf("EXEC = %#v", rows)
	}
	if err := s.SetAutoCommit(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "SET "+h+"b 1")
	if !s.InTx() {
		t.Error("manual mode did not open a transaction")
	}
	if err := s.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAutoCommit(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if v := value(t, s, "GET "+h+"b"); v != "1" {
		t.Errorf("committed = %#v", v)
	}
	for _, line := range []string{"SELECT 1", "WATCH " + h + "a"} {
		if _, err := s.Execute(t.Context(), driver.Statement{Text: line}); err == nil {
			t.Errorf("%s accepted", line)
		}
	}

	// A cancelled blocking command breaks the node connection; go-redis
	// redials it.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.Execute(ctx, driver.Statement{Text: "BLPOP " + h + "never 0"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("BLPOP = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("cancel took %v", d)
	}
	if v := value(t, s, "GET "+h+"a"); v != "2" {
		t.Errorf("after cancel = %#v", v)
	}

	// Editing goes through WATCH on the key's node.
	exec(t, s, "HSET "+p+"hash f 1")
	node := &driver.Node{Path: KeyPath(0, p+"hash", ""), Kind: driver.NodeKey}
	err = c.Apply(t.Context(), node, []driver.RowChange{{Op: driver.OpUpdate, Key: map[string]any{"field": "f"}, Values: map[string]any{"value": "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v := value(t, s, "HGET "+p+"hash f"); v != "2" {
		t.Errorf("edited = %#v", v)
	}
	if err := c.Ping(t.Context()); err != nil {
		t.Error(err)
	}
}
