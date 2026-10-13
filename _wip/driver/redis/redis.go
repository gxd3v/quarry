// Package redis implements the Redis driver on top of go-redis. One
// connection works with a single server, a Sentinel-managed primary or a
// Cluster.
//
// # Connection settings
//
// driver.Config maps as follows. Host is one host or several comma-separated
// "host[:port]" entries; entries without a port use Port (default 6379, or
// 26379 for Sentinel). Database is the logical database index ("" = 0). User
// and the password authenticate with AUTH/ACL. Options:
//
//	cluster=true          Redis Cluster (implied by several hosts without master)
//	master=<name>         Sentinel: hosts are sentinels, <name> the primary
//	sentinel_user=, sentinel_password=   credentials for the sentinels
//	tls=true              TLS (rediss://); tls_skip_verify=true skips verification;
//	                      tls_server_name= overrides the SNI/verification name
//	separator=<s>         key tree separator (default ":")
//	connect_timeout=<n>   seconds or a Go duration (default 10s)
//	protocol=2            force RESP2 (default RESP3, with fallback)
//
// # Explorer tree
//
// Roots are the logical databases, Path [index] ("0", "1", ...), Name "db0".
// Below a database, keys are grouped into folders by the separator. Path
// elements after the index are folder segments in order; a key's last
// element is keyMarker followed by the whole key name, and a folder segment
// that happens to start with the NUL byte is prefixed with folderEscape, so
// a folder and a key with the same name never share a Path. For key "a:b:c"
// the folders are [db, "a"] and [db, "a", "b"] and the key is
// [db, "a", "b", "\x00k:a:b:c"]. Key nodes carry "type · ttl Ns" in Detail.
//
// # Statements
//
// One command per line, see Split and Fields. Replies become tables by shape:
// see tabulate.
package redis

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

const (
	defaultHost         = "localhost"
	defaultPort         = 6379
	defaultSentinelPort = 26379
	defaultTimeout      = 10 * time.Second
	defaultSeparator    = ":"
	clientName          = "quarry"
)

var (
	errClosed   = errors.New("connection is closed")
	errReadOnly = errors.New("connection is read-only: changes cannot be saved")
)

type redisDriver struct{}

// New returns the Redis driver.
func New() driver.Driver { return redisDriver{} }

func (redisDriver) Kind() driver.Kind         { return driver.Redis }
func (redisDriver) Language() driver.Language { return driver.LangRedis }

func (redisDriver) Open(ctx context.Context, cfg driver.Config, password string) (driver.Conn, error) {
	c, err := newConn(cfg, password)
	if err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.DialTimeout+5*time.Second)
		defer cancel()
	}
	if err := c.base.Ping(ctx).Err(); err != nil {
		c.Close()
		return nil, connectError(err, c.where())
	}
	return c, nil
}

// conn serves explorer metadata through one go-redis client per logical
// database (Cluster: a single client, database 0 only).
type conn struct {
	opts     goredis.UniversalOptions
	tls      *tls.Config
	cluster  bool
	defDB    int
	sep      string
	readOnly bool

	base   goredis.UniversalClient
	mu     sync.Mutex
	dbs    map[int]goredis.UniversalClient
	closed atomic.Bool
}

func newConn(cfg driver.Config, password string) (*conn, error) {
	opt := func(k string) string { return strings.TrimSpace(cfg.Options[k]) }
	c := &conn{
		sep:      cmp.Or(cfg.Options["separator"], defaultSeparator),
		readOnly: cfg.ReadOnly,
		dbs:      map[int]goredis.UniversalClient{},
	}
	if s := strings.TrimSpace(cfg.Database); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid Redis database %q: want an index like 0", cfg.Database)
		}
		c.defDB = n
	}
	timeout := defaultTimeout
	if v := opt("connect_timeout"); v != "" {
		d, err := parseTimeout(v)
		if err != nil {
			return nil, err
		}
		timeout = d
	}
	master := opt("master")
	port := cfg.Port
	if port == 0 {
		port = defaultPort
		if master != "" {
			port = defaultSentinelPort
		}
	}
	addrs, err := addresses(cfg.Host, port)
	if err != nil {
		return nil, err
	}
	c.cluster = master == "" && (isTrue(opt("cluster")) || len(addrs) > 1)
	if c.cluster && c.defDB != 0 {
		return nil, errors.New("Redis Cluster only has database 0")
	}
	if isTrue(opt("tls")) {
		c.tls = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: isTrue(opt("tls_skip_verify")),
			ServerName:         opt("tls_server_name"),
		}
	}
	proto := 3
	if v := opt("protocol"); v != "" {
		if v != "2" && v != "3" {
			return nil, fmt.Errorf("invalid protocol %q: want 2 or 3", v)
		}
		proto, _ = strconv.Atoi(v)
	}
	c.opts = goredis.UniversalOptions{
		Addrs:            addrs,
		ClientName:       clientName,
		DB:               c.defDB,
		Protocol:         proto,
		Username:         cfg.User,
		Password:         password,
		SentinelUsername: opt("sentinel_user"),
		SentinelPassword: opt("sentinel_password"),
		MasterName:       master,
		IsClusterMode:    c.cluster,
		DialTimeout:      timeout,
		ReadTimeout:      30 * time.Second,
		WriteTimeout:     30 * time.Second,
		PoolSize:         4,
		MaxRetries:       1,
		// Metadata calls get deadlines from their contexts.
		ContextTimeoutEnabled: true,
		DisableIdentity:       true,
	}
	c.opts.Dialer = c.dialer(nil)
	c.base = goredis.NewUniversalClient(&c.opts)
	if !c.cluster {
		c.dbs[c.defDB] = c.base
	}
	return c, nil
}

// addresses expands "h1,h2:7001,[::1]:7002" into host:port entries.
func addresses(hosts string, port int) ([]string, error) {
	var out []string
	for _, h := range strings.Split(hosts, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			if len(out) > 0 || strings.Contains(hosts, ",") {
				continue
			}
			h = defaultHost
		}
		host, p, err := splitHostPort(h)
		if err != nil {
			return nil, err
		}
		if p == "" {
			p = strconv.Itoa(port)
		} else if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid port %q", p)
		}
		out = append(out, net.JoinHostPort(cmp.Or(host, defaultHost), p))
	}
	if len(out) == 0 {
		out = []string{net.JoinHostPort(defaultHost, strconv.Itoa(port))}
	}
	return out, nil
}

func splitHostPort(s string) (host, port string, err error) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", fmt.Errorf("missing ']' in host %q", s)
		}
		rest := s[end+1:]
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("invalid host %q", s)
		}
		return s[1:end], strings.TrimPrefix(rest, ":"), nil
	}
	if strings.Count(s, ":") == 1 {
		host, port, _ = strings.Cut(s, ":")
		return host, port, nil
	}
	return s, "", nil
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on", "require", "required":
		return true
	}
	return false
}

func parseTimeout(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return d, nil
	}
	return 0, fmt.Errorf("invalid connect_timeout %q", v)
}

// dialer dials TCP (and TLS when configured); track, when set, receives
// every new connection so a session can abort it.
func (c *conn) dialer(track func(*killConn)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: c.opts.DialTimeout, KeepAlive: 30 * time.Second}
		nc, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if c.tls != nil {
			cfg := c.tls.Clone()
			if cfg.ServerName == "" {
				host, _, _ := net.SplitHostPort(addr)
				cfg.ServerName = host
			}
			tc := tls.Client(nc, cfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				nc.Close()
				return nil, err
			}
			nc = tc
		}
		if track == nil {
			return nc, nil
		}
		kc := &killConn{Conn: nc}
		track(kc)
		return kc, nil
	}
}

// killConn lets a session abort a blocked read or write: once killed, every
// deadline go-redis sets is replaced by one in the past.
type killConn struct {
	net.Conn
	killed atomic.Bool
}

var past = time.Unix(1, 0)

func (k *killConn) kill() {
	k.killed.Store(true)
	k.Conn.SetDeadline(past)
}

func (k *killConn) SetDeadline(t time.Time) error {
	if k.killed.Load() {
		t = past
	}
	return k.Conn.SetDeadline(t)
}

func (k *killConn) SetReadDeadline(t time.Time) error {
	if k.killed.Load() {
		t = past
	}
	return k.Conn.SetReadDeadline(t)
}

func (k *killConn) SetWriteDeadline(t time.Time) error {
	if k.killed.Load() {
		t = past
	}
	return k.Conn.SetWriteDeadline(t)
}

func (c *conn) where() string { return strings.Join(c.opts.Addrs, ",") }

// client returns the metadata client of logical database db.
func (c *conn) client(db int) (goredis.UniversalClient, error) {
	if c.closed.Load() {
		return nil, errClosed
	}
	if c.cluster {
		if db != 0 {
			return nil, errors.New("Redis Cluster only has database 0")
		}
		return c.base, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil, errClosed
	}
	if cl := c.dbs[db]; cl != nil {
		return cl, nil
	}
	o := c.opts
	o.DB = db
	o.PoolSize = 2
	cl := goredis.NewUniversalClient(&o)
	c.dbs[db] = cl
	return cl, nil
}

// dbIndex parses a database path element or scope ("" = the default).
func (c *conn) dbIndex(s string) (int, error) {
	if s == "" {
		return c.defDB, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "db"))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid Redis database %q", s)
	}
	return n, nil
}

func (c *conn) Close() error {
	c.mu.Lock()
	if c.closed.Swap(true) {
		c.mu.Unlock()
		return nil
	}
	dbs := c.dbs
	c.dbs = map[int]goredis.UniversalClient{}
	c.mu.Unlock()
	var errs []error
	if c.cluster {
		errs = append(errs, c.base.Close())
	}
	for _, cl := range dbs {
		errs = append(errs, cl.Close())
	}
	return errors.Join(errs...)
}

// connectError rewrites connect failures into something a status bar can
// show; the original error stays reachable with errors.As.
func connectError(err error, where string) error {
	if re, ok := errors.AsType[goredis.Error](err); ok {
		msg := re.Error()
		switch {
		case strings.HasPrefix(msg, "NOAUTH"):
			msg = "authentication required: set a password for this connection"
		case strings.HasPrefix(msg, "WRONGPASS"), strings.Contains(msg, "invalid password"),
			strings.Contains(msg, "invalid username-password"):
			msg = "authentication failed: wrong user name or password"
		}
		return &friendlyError{msg: msg, err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return &friendlyError{msg: "timed out connecting to " + where, err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &friendlyError{msg: "connection attempt canceled", err: err}
	}
	if de, ok := errors.AsType[*net.DNSError](err); ok {
		return &friendlyError{msg: fmt.Sprintf("cannot resolve host %q: %s", de.Name, de.Err), err: err}
	}
	if oe, ok := errors.AsType[*net.OpError](err); ok {
		return &friendlyError{msg: fmt.Sprintf("cannot connect to %s: %v", where, oe.Err), err: err}
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return &friendlyError{msg: "cannot connect to " + where + ": the server does not speak TLS", err: err}
	}
	msg := strings.TrimPrefix(err.Error(), "redis: ")
	if err == goredis.ErrClosed || errors.Is(err, net.ErrClosed) {
		msg = "the connection was closed"
	}
	return &friendlyError{msg: "cannot connect to " + where + ": " + msg, err: err}
}

func isTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

type friendlyError struct {
	msg string
	err error
}

func (e *friendlyError) Error() string { return e.msg }
func (e *friendlyError) Unwrap() error { return e.err }

// queryError converts a server error reply to *driver.QueryError (Code is
// the reply's leading upper-case word, like ERR or WRONGTYPE); other errors
// are returned as they are.
func queryError(err error) error {
	if err == nil {
		return nil
	}
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		return qe
	}
	re, ok := errors.AsType[goredis.Error](err)
	if !ok || err == goredis.Nil {
		return err
	}
	msg := re.Error()
	code, rest, _ := strings.Cut(msg, " ")
	if code == "" || strings.ToUpper(code) != code || strings.ContainsAny(code, "'\":") {
		return &driver.QueryError{Msg: msg, Position: -1}
	}
	return &driver.QueryError{Msg: cmp.Or(rest, msg), Code: code, Position: -1}
}
