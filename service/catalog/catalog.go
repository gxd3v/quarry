// Package catalog caches the object catalogs of live connections, shared by
// the explorer and the query service's autocomplete.
package catalog

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

// DefaultTTL is how long a loaded catalog is served before it is reloaded.
const DefaultTTL = 5 * time.Minute

// loadTimeout bounds a load, which outlives the callers waiting for it.
const loadTimeout = time.Minute

// ErrUnsupported is returned for connections that do not implement driver.Catalog.
var ErrUnsupported = errors.New("the connection does not provide an object catalog")

type Option func(*Cache)

// WithTTL sets how long a loaded catalog is served.
func WithTTL(d time.Duration) Option { return func(c *Cache) { c.ttl = d } }

// Cache holds driver.Catalog.Objects per connection and scope. Entries expire
// after the TTL and are dropped when the connection is reopened; failed loads
// are not kept. Returned slices are shared and must not be modified.
type Cache struct {
	live service.Live
	ttl  time.Duration

	mu      sync.Mutex
	entries map[key]*entry
}

type key struct {
	connID string
	scope  driver.Scope
}

// entry is one load from conn; its results are set before done is closed.
type entry struct {
	conn driver.Conn
	done chan struct{}
	objs []driver.Object
	err  error
	at   time.Time
}

func New(live service.Live, opts ...Option) *Cache {
	c := &Cache{live: live, ttl: DefaultTTL, entries: map[key]*entry{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Objects returns the catalog of connID at scope, loading it when missing,
// expired, loaded from a previous connection or when refresh is set.
// Concurrent callers share one load; a caller whose ctx ends stops waiting
// without canceling the load. Failures of service.Live are returned as
// Connect errors (service.LiveError).
func (c *Cache) Objects(ctx context.Context, connID string, scope driver.Scope, refresh bool) ([]driver.Object, error) {
	conn, _, err := c.live.Live(connID)
	if err != nil {
		return nil, service.LiveError(err)
	}
	cat, ok := conn.(driver.Catalog)
	if !ok {
		return nil, ErrUnsupported
	}
	k := key{connID, scope}
	c.mu.Lock()
	e := c.entries[k]
	if e == nil || e.conn != conn || (loaded(e) && (refresh || time.Since(e.at) >= c.ttl)) {
		e = c.load(ctx, k, conn, cat)
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e.objs, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Invalidate drops every catalog of connID.
func (c *Cache) Invalidate(connID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.connID == connID {
			delete(c.entries, k)
		}
	}
}

func loaded(e *entry) bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// load starts loading k into a new entry; c.mu must be held.
func (c *Cache) load(ctx context.Context, k key, conn driver.Conn, cat driver.Catalog) *entry {
	for ok, old := range c.entries {
		if loaded(old) && time.Since(old.at) >= c.ttl {
			delete(c.entries, ok)
		}
	}
	e := &entry{conn: conn, done: make(chan struct{})}
	c.entries[k] = e
	go func() {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		objs, err := cat.Objects(lctx, k.scope)
		c.mu.Lock()
		e.objs, e.err, e.at = objs, err, time.Now()
		if err != nil && c.entries[k] == e {
			delete(c.entries, k)
		}
		c.mu.Unlock()
		close(e.done)
	}()
	return e
}
