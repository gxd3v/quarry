// Package postgres implements the PostgreSQL driver on top of pgx.
package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/gxd3v/quarry/internal/driver"
)

const (
	defaultHost    = "localhost"
	defaultPort    = 5432
	defaultDB      = "postgres"
	defaultTimeout = 10 * time.Second
	defaultAppName = "quarry"

	// After a cancel request the socket gets this long to deliver the
	// server's answer before the connection is torn down.
	cancelDeadline = 5 * time.Second
	closeTimeout   = 3 * time.Second
)

var errClosed = errors.New("connection is closed")

type pgDriver struct{}

func New() driver.Driver { return pgDriver{} }

func (pgDriver) Kind() driver.Kind         { return driver.Postgres }
func (pgDriver) Language() driver.Language { return driver.LangSQL }

func (pgDriver) Open(ctx context.Context, cfg driver.Config, password string) (driver.Conn, error) {
	base, err := buildConfig(cfg, password)
	if err != nil {
		return nil, err
	}
	c := &conn{base: base, meta: map[string]*metaConn{}, readOnly: cfg.ReadOnly}
	err = c.with(ctx, base.Database, func(pc *pgx.Conn) error { return pc.Ping(ctx) })
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// buildConfig turns a saved connection into a pgx config. Values are passed
// through a quoted keyword/value string so user input can never inject extra
// settings.
func buildConfig(cfg driver.Config, password string) (*pgx.ConnConfig, error) {
	host := cmp.Or(cfg.Host, defaultHost)
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	timeout := defaultTimeout
	if v := cfg.Options["connect_timeout"]; v != "" {
		d, err := parseTimeout(v)
		if err != nil {
			return nil, err
		}
		timeout = d
	}
	kv := [][2]string{
		{"host", host},
		{"port", strconv.Itoa(port)},
		{"dbname", cmp.Or(cfg.Database, defaultDB)},
		{"user", cfg.User},
		{"password", password},
		{"application_name", cmp.Or(cfg.Options["application_name"], defaultAppName)},
		{"connect_timeout", strconv.Itoa(max(int(timeout.Round(time.Second)/time.Second), 1))},
	}
	for _, k := range []string{"sslmode", "sslrootcert", "sslcert", "sslkey"} {
		kv = append(kv, [2]string{k, cfg.Options[k]})
	}
	var b strings.Builder
	for _, p := range kv {
		if p[1] == "" {
			continue
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(quoteValue(p[1]))
		b.WriteByte(' ')
	}
	pc, err := pgx.ParseConfig(b.String())
	if err != nil {
		// ParseConfigError's own text embeds the connection string; only
		// surface the underlying cause so the password never leaks.
		if pe, ok := errors.AsType[*pgconn.ParseConfigError](err); ok {
			if inner := pe.Unwrap(); inner != nil {
				return nil, fmt.Errorf("invalid connection settings: %w", inner)
			}
		}
		return nil, errors.New("invalid connection settings")
	}
	pc.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: cancelDeadline}
	}
	// One round trip per statement, no named prepared statements: works
	// through poolers and never trips over stale cached plans.
	pc.DefaultQueryExecMode = pgx.QueryExecModeExec
	pc.StatementCacheCapacity = 0
	pc.DescriptionCacheCapacity = 0
	if pc.RuntimeParams == nil {
		pc.RuntimeParams = map[string]string{}
	}
	pc.RuntimeParams["datestyle"] = "ISO"
	return pc, nil
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

// quoteValue quotes a value for a libpq keyword/value connection string.
func quoteValue(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// conn serves explorer metadata. PostgreSQL catalogs are per database, so it
// keeps one lazily opened connection per database, each serialized by its own
// mutex.
type conn struct {
	base   *pgx.ConnConfig
	mu     sync.Mutex
	meta   map[string]*metaConn
	closed atomic.Bool
	// readOnly (driver.Config.ReadOnly) makes every session read-only and
	// disables Apply.
	readOnly bool

	typesMu sync.Mutex
	types   map[string]map[uint32]typeInfo // per database, see typeInfos
}

type metaConn struct {
	mu sync.Mutex
	pc *pgx.Conn
}

func (c *conn) connect(ctx context.Context, db string, onNotice pgconn.NoticeHandler, rt map[string]string) (*pgx.Conn, error) {
	cfg := c.base.Copy()
	cfg.Database = cmp.Or(db, c.base.Database)
	cfg.OnNotice = onNotice
	maps.Copy(cfg.RuntimeParams, rt)
	pc, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, connectError(err, cfg)
	}
	return pc, nil
}

// with runs fn on the metadata connection of db, opening it if needed.
func (c *conn) with(ctx context.Context, db string, fn func(*pgx.Conn) error) error {
	db = cmp.Or(db, c.base.Database)
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return errClosed
	}
	m := c.meta[db]
	if m == nil {
		m = &metaConn{}
		c.meta[db] = m
	}
	c.mu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if c.closed.Load() {
		return errClosed
	}
	if m.pc == nil || m.pc.IsClosed() {
		pc, err := c.connect(ctx, db, nil, nil)
		if err != nil {
			return err
		}
		m.pc = pc
	}
	if err := fn(m.pc); err != nil {
		return queryError(err, "")
	}
	return nil
}

func (c *conn) Close() error {
	c.mu.Lock()
	c.closed.Store(true)
	metas := c.meta
	c.meta = map[string]*metaConn{}
	c.mu.Unlock()

	var errs []error
	for _, m := range metas {
		m.mu.Lock()
		if m.pc != nil {
			ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			errs = append(errs, m.pc.Close(ctx))
			cancel()
			m.pc = nil
		}
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// connectError rewrites pgx connect failures into something a status bar
// can show; the original error stays reachable with errors.As.
func connectError(err error, cfg *pgx.ConnConfig) error {
	where := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok {
		return &friendlyError{msg: pe.Message, err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return &friendlyError{msg: "timed out connecting to " + where, err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &friendlyError{msg: "connection attempt canceled", err: err}
	}
	if oe, ok := errors.AsType[*net.OpError](err); ok {
		return &friendlyError{msg: fmt.Sprintf("cannot connect to %s: %v", where, oe.Err), err: err}
	}
	if de, ok := errors.AsType[*net.DNSError](err); ok {
		return &friendlyError{msg: fmt.Sprintf("cannot resolve host %q: %s", de.Name, de.Err), err: err}
	}
	return &friendlyError{msg: "cannot connect to " + where + ": " + innermost(err).Error(), err: err}
}

func isTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

type friendlyError struct {
	msg string
	err error
}

func (e *friendlyError) Error() string { return e.msg }
func (e *friendlyError) Unwrap() error { return e.err }
