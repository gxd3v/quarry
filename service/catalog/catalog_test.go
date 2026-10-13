package catalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

type fakeLive struct {
	mu    sync.Mutex
	conns map[string]driver.Conn
}

func (l *fakeLive) set(id string, c driver.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conns[id] = c
}

func (l *fakeLive) Live(id string) (driver.Conn, driver.Config, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.conns[id]
	switch {
	case !ok:
		return nil, driver.Config{}, connect.NewError(connect.CodeNotFound, errors.New("unknown connection"))
	case c == nil:
		return nil, driver.Config{}, service.ErrNotConnected
	}
	return c, driver.Config{Kind: driver.Postgres}, nil
}

func (l *fakeLive) Config(id string) (driver.Config, bool) { return driver.Config{}, true }

type plainConn struct{}

func (plainConn) Children(context.Context, *driver.Node) ([]*driver.Node, error) { return nil, nil }
func (plainConn) NewSession(context.Context, driver.Scope) (driver.Session, error) {
	return nil, errors.New("no sessions")
}
func (plainConn) Close() error { return nil }

// catConn counts loads; while gate is set, loads wait for it to close.
type catConn struct {
	plainConn
	name string

	mu    sync.Mutex
	calls int
	gate  chan struct{}
	err   error
	ctxs  []context.Context
}

func (c *catConn) Objects(ctx context.Context, scope driver.Scope) ([]driver.Object, error) {
	c.mu.Lock()
	c.calls++
	gate, err := c.gate, c.err
	c.ctxs = append(c.ctxs, ctx)
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	return []driver.Object{{Schema: scope.Schema, Name: c.name, Kind: driver.NodeTable}}, nil
}

func (c *catConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newCache(conns map[string]driver.Conn, opts ...Option) (*Cache, *fakeLive) {
	l := &fakeLive{conns: conns}
	return New(l, opts...), l
}

func objects(t *testing.T, c *Cache, id string, scope driver.Scope, refresh bool) []driver.Object {
	t.Helper()
	objs, err := c.Objects(context.Background(), id, scope, refresh)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func TestCacheTTLAndRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := &catConn{name: "users"}
		c, _ := newCache(map[string]driver.Conn{"pg": conn}, WithTTL(time.Minute))
		s := driver.Scope{Database: "app", Schema: "shop"}

		if o := objects(t, c, "pg", s, false); len(o) != 1 || o[0].Name != "users" || o[0].Schema != "shop" {
			t.Fatalf("objects %v", o)
		}
		objects(t, c, "pg", s, false)
		if n := conn.count(); n != 1 {
			t.Fatalf("loads = %d, want 1", n)
		}
		objects(t, c, "pg", driver.Scope{Database: "app"}, false)
		if n := conn.count(); n != 2 {
			t.Fatalf("other scope: loads = %d, want 2", n)
		}

		time.Sleep(59 * time.Second)
		objects(t, c, "pg", s, false)
		if n := conn.count(); n != 2 {
			t.Fatalf("before expiry: loads = %d", n)
		}
		time.Sleep(time.Second)
		objects(t, c, "pg", s, false)
		if n := conn.count(); n != 3 {
			t.Fatalf("after expiry: loads = %d, want 3", n)
		}

		objects(t, c, "pg", s, true)
		if n := conn.count(); n != 4 {
			t.Fatalf("refresh: loads = %d, want 4", n)
		}
	})
}

func TestCacheSharesLoads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		conn := &catConn{name: "users", gate: gate}
		c, _ := newCache(map[string]driver.Conn{"pg": conn})

		var wg sync.WaitGroup
		results := make([][]driver.Object, 3)
		for i := range results {
			wg.Go(func() { results[i], _ = c.Objects(context.Background(), "pg", driver.Scope{}, false) })
		}
		// A caller that gives up does not cancel the load for the others.
		ctx, cancel := context.WithCancel(context.Background())
		var quitErr error
		wg.Go(func() { _, quitErr = c.Objects(ctx, "pg", driver.Scope{}, false) })
		synctest.Wait()
		cancel()
		synctest.Wait()
		if !errors.Is(quitErr, context.Canceled) {
			t.Fatalf("canceled caller: %v", quitErr)
		}
		conn.mu.Lock()
		loadCtx := conn.ctxs[0]
		conn.mu.Unlock()
		if loadCtx.Err() != nil {
			t.Fatal("load context canceled with a caller")
		}
		close(gate)
		wg.Wait()
		for i, r := range results {
			if len(r) != 1 {
				t.Errorf("caller %d: %v", i, r)
			}
		}
		if n := conn.count(); n != 1 {
			t.Fatalf("loads = %d, want 1", n)
		}

		// A refresh while loading joins the load in flight.
		conn.mu.Lock()
		conn.gate = make(chan struct{})
		conn.mu.Unlock()
		wg.Go(func() { c.Objects(context.Background(), "pg", driver.Scope{}, true) })
		synctest.Wait()
		wg.Go(func() { c.Objects(context.Background(), "pg", driver.Scope{}, true) })
		synctest.Wait()
		close(conn.gate)
		wg.Wait()
		if n := conn.count(); n != 2 {
			t.Fatalf("loads = %d, want 2", n)
		}
	})
}

func TestCacheAbandonedLoadIsKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		conn := &catConn{name: "users", gate: gate}
		c, _ := newCache(map[string]driver.Conn{"pg": conn})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := c.Objects(ctx, "pg", driver.Scope{}, false); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		close(gate)
		synctest.Wait()
		objects(t, c, "pg", driver.Scope{}, false)
		if n := conn.count(); n != 1 {
			t.Fatalf("loads = %d, want 1", n)
		}
	})
}

func TestCacheErrorsNotKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := &catConn{name: "users", err: errors.New("permission denied")}
		c, _ := newCache(map[string]driver.Conn{"pg": conn})
		if _, err := c.Objects(context.Background(), "pg", driver.Scope{}, false); err == nil || err.Error() != "permission denied" {
			t.Fatalf("err = %v", err)
		}
		conn.mu.Lock()
		conn.err = nil
		conn.mu.Unlock()
		if o := objects(t, c, "pg", driver.Scope{}, false); len(o) != 1 {
			t.Fatalf("objects %v", o)
		}
		if n := conn.count(); n != 2 {
			t.Fatalf("loads = %d, want 2", n)
		}
	})
}

func TestCacheConnChangeAndInvalidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := &catConn{name: "old"}
		c, live := newCache(map[string]driver.Conn{"pg": old, "other": &catConn{name: "x"}})
		objects(t, c, "pg", driver.Scope{}, false)
		objects(t, c, "other", driver.Scope{}, false)

		reopened := &catConn{name: "new"}
		live.set("pg", reopened)
		if o := objects(t, c, "pg", driver.Scope{}, false); o[0].Name != "new" {
			t.Fatalf("after reopen: %v", o)
		}
		objects(t, c, "pg", driver.Scope{}, false)
		if reopened.count() != 1 {
			t.Fatalf("loads on reopened conn = %d", reopened.count())
		}

		c.Invalidate("pg")
		objects(t, c, "pg", driver.Scope{}, false)
		objects(t, c, "other", driver.Scope{}, false)
		if reopened.count() != 2 {
			t.Fatalf("loads after invalidate = %d, want 2", reopened.count())
		}
		if n := c.live.(*fakeLive).conns["other"].(*catConn).count(); n != 1 {
			t.Fatalf("other conn reloaded: %d", n)
		}
	})
}

func TestCacheErrors(t *testing.T) {
	c, _ := newCache(map[string]driver.Conn{"plain": plainConn{}, "closed": nil})
	ctx := context.Background()
	if _, err := c.Objects(ctx, "plain", driver.Scope{}, false); !errors.Is(err, ErrUnsupported) {
		t.Errorf("plain: %v", err)
	}
	if _, err := c.Objects(ctx, "closed", driver.Scope{}, false); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("closed: %v", err)
	}
	if _, err := c.Objects(ctx, "unknown", driver.Scope{}, false); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown: %v", err)
	}
}
