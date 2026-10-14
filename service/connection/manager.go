// Package connection manages saved connections and their live state, and
// serves the ConnectionService.
package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/internal/tunnel"
	"github.com/gxd3v/quarry/service"
)

const (
	defaultPingInterval = 15 * time.Second
	pingTimeout         = 10 * time.Second
)

type pinger interface {
	Ping(ctx context.Context) error
}

// Option configures a Manager.
type Option func(*Manager)

// WithLookup replaces driver.Lookup, mainly for tests.
func WithLookup(lookup func(driver.Kind) (driver.Driver, bool)) Option {
	return func(m *Manager) { m.lookup = lookup }
}

// WithPingInterval sets how often open connections are pinged (default 15s).
func WithPingInterval(d time.Duration) Option {
	return func(m *Manager) { m.every = d }
}

// Manager owns the saved connections (persisted to a TOML file, passwords in
// Secrets) and the live ones, pings them and broadcasts status changes.
type Manager struct {
	path    string
	secrets store.Secrets
	lookup  func(driver.Kind) (driver.Driver, bool)
	every   time.Duration

	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup // opens and ping loops

	saveMu sync.Mutex // serializes changes to the saved configs

	mu       sync.Mutex
	cfgs     []driver.Config
	states   map[uuid.UUID]*state
	watchers map[*watcher]struct{}
	closed   bool
}

var _ service.Live = (*Manager)(nil)

type state struct {
	status  *quarryv1.ConnectionStatus // replaced on change, never mutated
	live    *liveConn
	pending *attempt
}

type liveConn struct {
	conn driver.Conn
	tun  *tunnel.Tunnel     // SSH tunnel conn goes through, or nil
	stop context.CancelFunc // stops the ping loop
}

// close closes the connection, then its tunnel.
func (lc *liveConn) close() error {
	err := lc.conn.Close()
	if lc.tun != nil {
		err = errors.Join(err, lc.tun.Close())
	}
	return err
}

// attempt is an open in progress; concurrent Connect calls wait on it.
type attempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	status *quarryv1.ConnectionStatus
	code   connect.Code
	err    error
}

func (a *attempt) wait(ctx context.Context) (*quarryv1.ConnectionStatus, error) {
	select {
	case <-a.done:
		if a.err != nil {
			return nil, connect.NewError(a.code, a.err)
		}
		return a.status, nil
	case <-ctx.Done():
		return nil, ctxError(ctx.Err())
	}
}

// New loads the connections saved at path.
func New(path string, secrets store.Secrets, opts ...Option) (*Manager, error) {
	cfgs, err := store.LoadConnections(path)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		path:     path,
		secrets:  secrets,
		lookup:   driver.Lookup,
		every:    defaultPingInterval,
		cfgs:     cfgs,
		states:   make(map[uuid.UUID]*state, len(cfgs)),
		watchers: map[*watcher]struct{}{},
	}
	for _, o := range opts {
		o(m)
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	for _, c := range cfgs {
		m.states[c.ID] = &state{status: newStatus(c.ID, quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED, 0, nil)}
	}
	return m, nil
}

// Close closes every live connection, aborts pending opens and ends the
// status streams.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	var conns []*liveConn
	for _, st := range m.states {
		if lc := teardown(st); lc != nil {
			conns = append(conns, lc)
		}
	}
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
	var errs []error
	for _, lc := range conns {
		errs = append(errs, lc.close())
	}
	return errors.Join(errs...)
}

// Live implements service.Live. A LOST connection is still returned: the
// driver reconnects on the next call.
func (m *Manager) Live(id string) (driver.Conn, driver.Config, error) {
	uid, err := parseID(id)
	if err != nil {
		return nil, driver.Config{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.configLocked(uid)
	if !ok {
		return nil, driver.Config{}, notFound(uid)
	}
	st := m.states[uid]
	if st.live == nil {
		return nil, cfg, fmt.Errorf("%s: %w", cfg.Name, service.ErrNotConnected)
	}
	return st.live.conn, cfg, nil
}

// Config implements service.Live.
func (m *Manager) Config(id string) (driver.Config, bool) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return driver.Config{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configLocked(uid)
}

func (m *Manager) configLocked(id uuid.UUID) (driver.Config, bool) {
	i := slices.IndexFunc(m.cfgs, func(c driver.Config) bool { return c.ID == id })
	if i < 0 {
		return driver.Config{}, false
	}
	c := m.cfgs[i]
	c.Options = maps.Clone(c.Options)
	return c, true
}

// entry is a saved connection with its status.
type entry struct {
	cfg    driver.Config
	status *quarryv1.ConnectionStatus
}

// OpenIDs lists the connections that are open (or reconnecting).
func (m *Manager) OpenIDs() []string {
	var ids []string
	for _, e := range m.list() {
		switch e.status.GetState() {
		case quarryv1.ConnectionState_CONNECTION_STATE_CONNECTED, quarryv1.ConnectionState_CONNECTION_STATE_LOST:
			ids = append(ids, e.cfg.ID.String())
		}
	}
	return ids
}

func (m *Manager) list() []entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]entry, len(m.cfgs))
	for i, c := range m.cfgs {
		c.Options = maps.Clone(c.Options)
		out[i] = entry{c, m.states[c.ID].status}
	}
	return out
}

func (m *Manager) status(id uuid.UUID) *quarryv1.ConnectionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[id]; st != nil {
		return st.status
	}
	return nil
}

func (m *Manager) hasPassword(id uuid.UUID) bool {
	_, err := m.secrets.Get(id)
	return err == nil
}

// save validates and stores cfg; a nil ID creates a new connection. It is
// all or nothing: when the password cannot be stored (or forgotten) the file
// is restored and CodeUnavailable returned.
func (m *Manager) save(cfg driver.Config, password *string, forget bool) (driver.Config, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.mu.Lock()
	old := slices.Clone(m.cfgs)
	m.mu.Unlock()

	i := -1
	if cfg.ID == uuid.Nil() {
		cfg.ID = uuid.New()
	} else if i = slices.IndexFunc(old, func(c driver.Config) bool { return c.ID == cfg.ID }); i < 0 {
		return cfg, notFound(cfg.ID)
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	if err := store.Validate(cfg, old); err != nil {
		return cfg, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if cfg.Port == 0 {
		cfg.Port = store.DefaultPort(cfg.Kind)
	}
	if len(cfg.Options) == 0 {
		cfg.Options = nil
	}

	next := slices.Clone(old)
	if i < 0 {
		next = append(next, cfg)
	} else {
		next[i] = cfg
	}
	if err := store.SaveConnections(m.path, next); err != nil {
		return cfg, connect.NewError(connect.CodeInternal, fmt.Errorf("saving connections: %w", err))
	}
	if err := m.storeSecret(cfg, password, forget); err != nil {
		if rerr := store.SaveConnections(m.path, old); rerr != nil {
			return cfg, connect.NewError(connect.CodeInternal, fmt.Errorf("password not saved (%w) and the connection could not be restored: %w", err, rerr))
		}
		return cfg, connect.NewError(connect.CodeUnavailable, fmt.Errorf("connection not saved: password could not be stored: %w", err))
	}

	m.mu.Lock()
	m.cfgs = next
	var stale *liveConn
	if i < 0 {
		st := &state{}
		m.states[cfg.ID] = st
		m.setStatus(st, newStatus(cfg.ID, quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED, 0, nil))
	} else if st := m.states[cfg.ID]; !sameTarget(old[i], cfg) && st.status.State != quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED {
		stale = teardown(st)
		m.setStatus(st, newStatus(cfg.ID, quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED, 0, nil))
	}
	m.mu.Unlock()
	if stale != nil {
		stale.close()
	}
	cfg.Options = maps.Clone(cfg.Options)
	return cfg, nil
}

// storeSecret applies the password part of a save. Removing the secret of an
// ask_password connection is best effort: it is never read, and a missing
// keyring must not prevent saving.
func (m *Manager) storeSecret(cfg driver.Config, password *string, forget bool) error {
	switch {
	case forget:
		return m.secrets.Delete(cfg.ID)
	case cfg.AskPassword:
		if err := m.secrets.Delete(cfg.ID); err != nil && !errors.Is(err, store.ErrKeyringUnavailable) {
			return err
		}
	case password != nil:
		return m.secrets.Set(cfg.ID, *password)
	}
	return nil
}

// sameTarget reports whether a live connection opened with a stays valid
// for b.
func sameTarget(a, b driver.Config) bool {
	return a.Kind == b.Kind && a.Host == b.Host && a.Port == b.Port && a.User == b.User &&
		a.Database == b.Database && a.ReadOnly == b.ReadOnly && maps.Equal(a.Options, b.Options)
}

func (m *Manager) remove(id uuid.UUID) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.mu.Lock()
	old := slices.Clone(m.cfgs)
	m.mu.Unlock()
	i := slices.IndexFunc(old, func(c driver.Config) bool { return c.ID == id })
	if i < 0 {
		return notFound(id)
	}
	next := slices.Delete(slices.Clone(old), i, i+1)
	if err := store.SaveConnections(m.path, next); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("saving connections: %w", err))
	}

	m.mu.Lock()
	m.cfgs = next
	stale := teardown(m.states[id])
	delete(m.states, id)
	for w := range m.watchers {
		w.put(id.String(), nil)
	}
	m.mu.Unlock()
	if stale != nil {
		stale.close()
	}
	// Best effort: ids are never reused, so a leftover secret is harmless.
	m.secrets.Delete(id)
	m.secrets.Delete(sshSecretID(id, sshPassword))
	m.secrets.Delete(sshSecretID(id, sshPassphrase))
	return nil
}

// password resolves the password to open cfg with.
func (m *Manager) password(cfg driver.Config, given *string) (string, error) {
	if given != nil {
		return *given, nil
	}
	if cfg.AskPassword {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("password required"))
	}
	if cfg.ID == uuid.Nil() {
		return "", nil
	}
	pw, err := m.secrets.Get(cfg.ID)
	switch {
	case err == nil:
		return pw, nil
	case errors.Is(err, store.ErrNoSecret):
		// No password: trust/peer auth, Redis without AUTH, ...
		return "", nil
	default:
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("password required: %w", err))
	}
}

func (m *Manager) driverFor(k driver.Kind) (driver.Driver, error) {
	d, ok := m.lookup(k)
	switch {
	case ok:
	case !slices.Contains(kinds, k):
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Unknown database type %q", k))
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no driver for %s connections", k))
	}
	return d, nil
}

// connect opens id, or joins an open in progress. It never holds the lock
// while talking to the keyring or the server.
func (m *Manager) connect(ctx context.Context, id uuid.UUID, given *string) (*quarryv1.ConnectionStatus, error) {
	for {
		m.mu.Lock()
		cfg, st, err := m.stateLocked(id)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if st.live != nil {
			s := st.status
			m.mu.Unlock()
			return s, nil
		}
		if a := st.pending; a != nil {
			m.mu.Unlock()
			return a.wait(ctx)
		}
		m.mu.Unlock()

		d, err := m.driverFor(cfg.Kind)
		if err != nil {
			return nil, err
		}
		pw, err := m.password(cfg, given)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		now, st, err := m.stateLocked(id)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if st.live != nil || st.pending != nil || !sameTarget(cfg, now) || cfg.AskPassword != now.AskPassword {
			// Something changed while unlocked: start over.
			m.mu.Unlock()
			continue
		}
		actx, cancel := context.WithCancel(m.ctx)
		a := &attempt{done: make(chan struct{}), cancel: cancel}
		st.pending = a
		m.setStatus(st, newStatus(id, quarryv1.ConnectionState_CONNECTION_STATE_CONNECTING, 0, nil))
		m.wg.Add(1)
		m.mu.Unlock()
		go m.open(actx, a, now, d, pw)
		return a.wait(ctx)
	}
}

func (m *Manager) stateLocked(id uuid.UUID) (driver.Config, *state, error) {
	if m.closed {
		return driver.Config{}, nil, connect.NewError(connect.CodeUnavailable, errors.New("connection manager is closed"))
	}
	cfg, ok := m.configLocked(id)
	if !ok {
		return cfg, nil, notFound(id)
	}
	return cfg, m.states[id], nil
}

func (m *Manager) open(ctx context.Context, a *attempt, cfg driver.Config, d driver.Driver, pw string) {
	defer m.wg.Done()
	defer a.cancel()
	defer close(a.done)
	lc, latency, err := m.dial(ctx, d, cfg, pw)

	m.mu.Lock()
	st := m.states[cfg.ID]
	if st == nil || st.pending != a {
		// Disconnected, deleted, changed or closed meanwhile.
		m.mu.Unlock()
		if lc != nil {
			lc.close()
		}
		a.code, a.err = connect.CodeAborted, errors.New("connection attempt was cancelled")
		return
	}
	st.pending = nil
	if err != nil {
		a.status = newStatus(cfg.ID, quarryv1.ConnectionState_CONNECTION_STATE_FAILED, 0, err)
		m.setStatus(st, a.status)
		m.mu.Unlock()
		a.code, a.err = openCode(err), err
		return
	}
	lctx, stop := context.WithCancel(m.ctx)
	lc.stop = stop
	st.live = lc
	a.status = newStatus(cfg.ID, quarryv1.ConnectionState_CONNECTION_STATE_CONNECTED, latency, nil)
	m.setStatus(st, a.status)
	if p, ok := lc.conn.(pinger); ok {
		m.wg.Add(1)
		go m.pingLoop(lctx, cfg.ID, lc, p)
	}
	m.mu.Unlock()
}

// dial opens cfg, through an SSH tunnel when its options ask for one. The
// returned liveConn has no stop func yet.
func (m *Manager) dial(ctx context.Context, d driver.Driver, cfg driver.Config, pw string) (*liveConn, time.Duration, error) {
	tun, err := m.tunnel(ctx, &cfg)
	if err != nil {
		return nil, 0, err
	}
	c, latency, err := openConn(ctx, d, cfg, pw)
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, 0, err
	}
	return &liveConn{conn: c, tun: tun}, latency, nil
}

// tunnel opens the SSH tunnel cfg's options ask for, if any, and points cfg
// at its local end. Unknown host keys are trusted only with ssh_accept_new.
// Tunnel opens cfg's SSH tunnel, when it has one, and points cfg at it; the
// caller closes it. Used by native shells, which connect on their own.
func (m *Manager) Tunnel(ctx context.Context, cfg *driver.Config) (io.Closer, error) {
	t, err := m.tunnel(ctx, cfg)
	if t == nil {
		return nil, err
	}
	return t, nil
}

func (m *Manager) tunnel(ctx context.Context, cfg *driver.Config) (*tunnel.Tunnel, error) {
	tc, ok := tunnel.FromOptions(cfg.Options)
	if !ok {
		return nil, nil
	}
	if cfg.ID != uuid.Nil() {
		tc.Password = m.sshSecret(cfg.ID, sshPassword)
		tc.Passphrase = m.sshSecret(cfg.ID, sshPassphrase)
	}
	t, err := tunnel.Open(ctx, tc, net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if errors.Is(err, tunnel.ErrHostKeyUnknown) {
		return nil, fmt.Errorf("%w; it is accepted with the SSH option \"Trust an unknown host key on first connect\" (ssh_accept_new)", err)
	}
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port = "127.0.0.1", t.Port()
	return t, nil
}

const (
	sshPassword   = "password"
	sshPassphrase = "passphrase"
)

// sshSecretID derives the Secrets ID under which a connection's SSH password
// or key passphrase is kept: a version 8 UUID hashed from the connection ID.
func sshSecretID(id uuid.UUID, kind string) uuid.UUID {
	h := sha256.Sum256(append([]byte("quarry ssh "+kind+":"), id[:]...))
	var u uuid.UUID
	copy(u[:], h[:])
	u[6] = u[6]&0x0f | 0x80
	u[8] = u[8]&0x3f | 0x80
	return u
}

// sshSecret returns the stored SSH secret, or "" so that key and agent auth
// still run when there is none or the keyring is unavailable.
func (m *Manager) sshSecret(id uuid.UUID, kind string) string {
	s, err := m.secrets.Get(sshSecretID(id, kind))
	if err != nil {
		return ""
	}
	return s
}

// storeSSHSecrets stores the given SSH secrets; "" removes one, nil keeps it.
func (m *Manager) storeSSHSecrets(id uuid.UUID, password, passphrase *string) error {
	for kind, v := range map[string]*string{sshPassword: password, sshPassphrase: passphrase} {
		if v == nil {
			continue
		}
		var err error
		if *v == "" {
			if err = m.secrets.Delete(sshSecretID(id, kind)); errors.Is(err, store.ErrNoSecret) {
				err = nil
			}
		} else {
			err = m.secrets.Set(sshSecretID(id, kind), *v)
		}
		if err != nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("connection saved, but the SSH %s could not be stored: %w", kind, err))
		}
	}
	return nil
}

// openConn opens and, when supported, pings a connection. The latency is the
// ping round trip, or the open time for drivers without Ping.
func openConn(ctx context.Context, d driver.Driver, cfg driver.Config, pw string) (driver.Conn, time.Duration, error) {
	start := time.Now()
	c, err := d.Open(ctx, cfg, pw)
	if err != nil {
		return nil, 0, err
	}
	latency := time.Since(start)
	if p, ok := c.(pinger); ok {
		start = time.Now()
		if err := p.Ping(ctx); err != nil {
			c.Close()
			return nil, 0, err
		}
		latency = time.Since(start)
	}
	return c, latency, nil
}

func (m *Manager) pingLoop(ctx context.Context, id uuid.UUID, lc *liveConn, p pinger) {
	defer m.wg.Done()
	t := time.NewTicker(m.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		start := time.Now()
		err := p.Ping(pctx)
		latency := time.Since(start)
		cancel()
		if ctx.Err() != nil {
			return
		}
		s := newStatus(id, quarryv1.ConnectionState_CONNECTION_STATE_CONNECTED, latency, nil)
		if err != nil {
			s = newStatus(id, quarryv1.ConnectionState_CONNECTION_STATE_LOST, 0, err)
		}
		m.mu.Lock()
		if st := m.states[id]; st != nil && st.live == lc && !sameStatus(st.status, s) {
			m.setStatus(st, s)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) disconnect(id uuid.UUID) error {
	m.mu.Lock()
	_, st, err := m.stateLocked(id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	stale := teardown(st)
	if st.status.State != quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED {
		m.setStatus(st, newStatus(id, quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED, 0, nil))
	}
	m.mu.Unlock()
	if stale != nil {
		// Detached either way; a failing Close has nothing left to report.
		stale.close()
	}
	return nil
}

// teardown detaches the live connection (returned for closing outside the
// lock) and aborts a pending open. Callers set the new status.
func teardown(st *state) *liveConn {
	if a := st.pending; a != nil {
		a.cancel()
		st.pending = nil
	}
	lc := st.live
	if lc != nil {
		lc.stop()
		st.live = nil
	}
	return lc
}

// test opens a throwaway connection and returns its latency.
func (m *Manager) test(ctx context.Context, cfg driver.Config, given *string) (time.Duration, error) {
	if cfg.Port == 0 {
		cfg.Port = store.DefaultPort(cfg.Kind)
	}
	d, err := m.driverFor(cfg.Kind)
	if err != nil {
		return 0, err
	}
	pw, err := m.password(cfg, given)
	if err != nil {
		return 0, err
	}
	lc, latency, err := m.dial(ctx, d, cfg, pw)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctxError(ctx.Err())
		}
		return 0, connect.NewError(openCode(err), err)
	}
	lc.close()
	return latency, nil
}

func (m *Manager) setStatus(st *state, s *quarryv1.ConnectionStatus) {
	st.status = s
	for w := range m.watchers {
		w.put(s.Id, s)
	}
}

func newStatus(id uuid.UUID, s quarryv1.ConnectionState, latency time.Duration, err error) *quarryv1.ConnectionStatus {
	st := &quarryv1.ConnectionStatus{Id: id.String(), State: s}
	if latency > 0 {
		// Sub-millisecond round trips show as 1: 0 means unknown.
		st.LatencyMs = max(latency.Milliseconds(), 1)
	}
	if err != nil {
		st.Error = err.Error()
	}
	return st
}

func sameStatus(a, b *quarryv1.ConnectionStatus) bool {
	return a.State == b.State && a.LatencyMs == b.LatencyMs && a.Error == b.Error
}

func ctxError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeCanceled, err)
}

func notFound(id uuid.UUID) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no connection with id %s", id))
}

func parseID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return id, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid connection id %q", s))
	}
	return id, nil
}

// openCode classifies a driver Open failure: rejected credentials are
// Unauthenticated so the UI asks for the password again; a refused SSH login
// is PermissionDenied, since that password is not the one asked for.
func openCode(err error) connect.Code {
	if errors.Is(err, tunnel.ErrAuth) {
		return connect.CodePermissionDenied
	}
	if errors.Is(err, driver.ErrAuth) {
		return connect.CodeUnauthenticated
	}
	return connect.CodeUnavailable
}
