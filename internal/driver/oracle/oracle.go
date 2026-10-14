// Package oracle implements the Oracle driver on top of go-ora (pure Go, no
// Instant Client).
package oracle

import (
	"cmp"
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/configurations"
	"github.com/sijms/go-ora/v2/network"

	"github.com/gxd3v/quarry/internal/driver"
)

const (
	defaultHost    = "localhost"
	defaultPort    = 1521
	defaultTimeout = 10 * time.Second
	defaultProgram = "quarry"

	// After a cancel request a call gets this long to return before its
	// socket is closed.
	cancelDeadline = 5 * time.Second
	closeTimeout   = 3 * time.Second
)

var (
	errClosed = errors.New("connection is closed")
	errLost   = errors.New("connection lost; reconnect to continue")
)

type oraDriver struct{}

func New() driver.Driver { return oraDriver{} }

func (oraDriver) Kind() driver.Kind         { return driver.Oracle }
func (oraDriver) Language() driver.Language { return driver.LangSQL }

// Open connects to cfg. Database (or option service) is the service name,
// e.g. FREEPDB1; option sid connects by SID instead. Other options:
// connect_timeout (seconds or a Go duration), role (sysdba, sysoper, ...),
// tls/ssl=true (TCPS), ssl_verify=false, wallet (directory) and
// show_system_schemas=true (list Oracle-maintained schemas in the explorer).
func (oraDriver) Open(ctx context.Context, cfg driver.Config, password string) (driver.Conn, error) {
	st, err := newSettings(cfg, password)
	if err != nil {
		return nil, err
	}
	c := &conn{st: st, readOnly: cfg.ReadOnly, showSystem: cfg.Options["show_system_schemas"] == "true"}
	err = c.with(ctx, func(l *link) error {
		return l.queryRow(ctx, "SELECT USER FROM dual", nil, &c.user)
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// settings is a saved connection resolved into what go-ora needs. The
// password is kept apart from the URL so that parse errors cannot leak it.
type settings struct {
	host, service, sid, user, password string
	port                               int
	timeout                            time.Duration
	opts                               map[string]string
}

var (
	plainName = regexp.MustCompile(`^[A-Za-z0-9_.$#-]+$`)
	roles     = []string{"SYSDBA", "SYSOPER", "SYSASM", "SYSBACKUP", "SYSDG", "SYSKM", "SYSRAC"}
)

func newSettings(cfg driver.Config, password string) (*settings, error) {
	st := &settings{
		host:     cmp.Or(cfg.Host, defaultHost),
		port:     cmp.Or(cfg.Port, defaultPort),
		service:  cmp.Or(cfg.Options["service"], cfg.Database),
		sid:      cfg.Options["sid"],
		user:     cfg.User,
		password: password,
		timeout:  defaultTimeout,
		opts:     map[string]string{},
	}
	if st.sid != "" {
		st.service = ""
		if !plainName.MatchString(st.sid) {
			return nil, fmt.Errorf("invalid Oracle SID %q", st.sid)
		}
		st.opts["SID"] = st.sid
	} else if st.service == "" {
		return nil, errors.New("an Oracle service name (database) or SID is required")
	} else if !plainName.MatchString(st.service) {
		return nil, fmt.Errorf("invalid Oracle service name %q", st.service)
	}
	if v := cfg.Options["connect_timeout"]; v != "" {
		d, err := parseTimeout(v)
		if err != nil {
			return nil, err
		}
		st.timeout = d
	}
	if r := strings.ToUpper(strings.TrimSpace(cfg.Options["role"])); r != "" {
		if !slices.Contains(roles, r) {
			return nil, fmt.Errorf("unknown Oracle role %q", cfg.Options["role"])
		}
		st.opts["DBA PRIVILEGE"] = r
	}
	if cfg.Options["tls"] == "true" || cfg.Options["ssl"] == "true" {
		st.opts["SSL"] = "true"
		if cfg.Options["ssl_verify"] == "false" {
			st.opts["SSL VERIFY"] = "false"
		}
	}
	if w := cfg.Options["wallet"]; w != "" {
		st.opts["WALLET"] = w
	}
	return st, nil
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

func (st *settings) where() string {
	return net.JoinHostPort(st.host, strconv.Itoa(st.port)) + "/" + cmp.Or(st.service, st.sid)
}

// config builds a fresh go-ora configuration (go-ora keeps per-connection
// state in it) dialing through sock.
func (st *settings) config(sock *socket) (*configurations.ConnectionConfig, error) {
	url := go_ora.BuildUrl(st.host, st.port, st.service, st.user, "", st.opts)
	cfg, err := go_ora.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("invalid connection settings: %w", err)
	}
	cfg.DatabaseInfo.Password = st.password
	cfg.ConnectTimeout = st.timeout
	cfg.ProgramName = defaultProgram
	cfg.Dialer = sock
	return cfg, nil
}

// socket dials the server and keeps the connection, so that a call stuck on
// the network can be aborted from another goroutine and a lost connection
// noticed without a round trip.
type socket struct {
	timeout time.Duration
	mu      sync.Mutex
	nc      net.Conn
	dead    atomic.Bool
}

func (s *socket) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: s.timeout, KeepAlive: 30 * time.Second}
	nc, err := d.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.nc = nc
	s.mu.Unlock()
	return nc, nil
}

// abort closes the network connection, failing any call in progress.
func (s *socket) abort() {
	s.dead.Store(true)
	s.mu.Lock()
	if s.nc != nil {
		s.nc.Close()
	}
	s.mu.Unlock()
}

// link is one server session: a go-ora connection and its socket. Like
// go-ora's connection it is not safe for concurrent use.
type link struct {
	oc   *go_ora.Connection
	sock *socket
}

// dial opens a new server session.
func (st *settings) dial(ctx context.Context) (*link, error) {
	sock := &socket{timeout: st.timeout}
	cfg, err := st.config(sock)
	if err != nil {
		return nil, err
	}
	oc, err := go_ora.NewConnection("", cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid connection settings: %w", err)
	}
	if st.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, st.timeout+time.Second)
		defer cancel()
	}
	if err := oc.OpenWithContext(ctx); err != nil {
		sock.abort()
		return nil, connectError(err, st, ctx)
	}
	return &link{oc: oc, sock: sock}, nil
}

func (l *link) alive() bool { return !l.sock.dead.Load() }

// close logs off politely unless the socket is known to be dead.
func (l *link) close() error {
	if !l.alive() {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- l.oc.Close() }()
	select {
	case err := <-done:
		l.sock.dead.Store(true)
		return err
	case <-time.After(closeTimeout):
		l.sock.abort()
		return nil
	}
}

// check marks the link dead when err means the connection is gone.
func (l *link) check(err error) {
	if err != nil && isConnLost(err) {
		l.sock.abort()
	}
}

// conn serves explorer metadata on one lazily opened session, and cancels
// queries of other sessions through a second one when the user may.
type conn struct {
	st         *settings
	readOnly   bool
	showSystem bool
	user       string // the logged-in user, set by Open
	closed     atomic.Bool

	mu   sync.Mutex
	meta *link

	ctlMu sync.Mutex
	ctl   *link
}

// with runs fn on the metadata session, opening it if needed. A session
// found dead is replaced on the next call.
func (c *conn) with(ctx context.Context, fn func(*link) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return errClosed
	}
	if c.meta != nil && !c.meta.alive() {
		c.meta = nil
	}
	if c.meta == nil {
		l, err := c.st.dial(ctx)
		if err != nil {
			return err
		}
		c.meta = l
	}
	err := fn(c.meta)
	c.meta.check(err)
	if err != nil {
		return queryError(err, "")
	}
	return nil
}

func (c *conn) Close() error {
	c.closed.Store(true)
	var errs []error
	c.mu.Lock()
	if c.meta != nil {
		errs = append(errs, c.meta.close())
		c.meta = nil
	}
	c.mu.Unlock()
	c.ctlMu.Lock()
	if c.ctl != nil {
		errs = append(errs, c.ctl.close())
		c.ctl = nil
	}
	c.ctlMu.Unlock()
	return errors.Join(errs...)
}

// authCodes are the logon errors caused by rejected credentials.
var authCodes = []int{1005, 1017, 28000, 28001}

// connectError rewrites connect failures into something a status bar can
// show; the original error stays reachable with errors.As.
func connectError(err error, st *settings, ctx context.Context) error {
	where := st.where()
	if oe, ok := errors.AsType[*network.OracleError](err); ok {
		msg := firstLine(oe.ErrMsg)
		if slices.Contains(authCodes, oe.ErrCode) {
			return &friendlyError{msg: msg, err: fmt.Errorf("%w: %w", driver.ErrAuth, err)}
		}
		return &friendlyError{msg: msg, err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
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

// lostCodes are the server errors after which a session is unusable.
var lostCodes = []int{28, 1012, 1033, 1034, 1089, 2396, 3113, 3114, 3135, 12528, 12537}

// isConnLost reports whether err means the session's connection is gone.
func isConnLost(err error) bool {
	if oe, ok := errors.AsType[*network.OracleError](err); ok {
		return slices.Contains(lostCodes, oe.ErrCode)
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, errLost) || errors.Is(err, sqldriver.ErrBadConn)
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
