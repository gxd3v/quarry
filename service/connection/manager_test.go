package connection

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/service"
)

const (
	stDisconnected = quarryv1.ConnectionState_CONNECTION_STATE_DISCONNECTED
	stConnecting   = quarryv1.ConnectionState_CONNECTION_STATE_CONNECTING
	stConnected    = quarryv1.ConnectionState_CONNECTION_STATE_CONNECTED
	stLost         = quarryv1.ConnectionState_CONNECTION_STATE_LOST
	stFailed       = quarryv1.ConnectionState_CONNECTION_STATE_FAILED
)

type fakeDriver struct {
	kind driver.Kind
	lang driver.Language

	mu        sync.Mutex
	gate      chan struct{} // when set, Open waits for it
	openErr   error
	pingDelay time.Duration
	opens     int
	passwords []string
	conns     []*fakeConn
}

func (d *fakeDriver) Kind() driver.Kind { return d.kind }

func (d *fakeDriver) Language() driver.Language { return d.lang }

func (d *fakeDriver) Open(ctx context.Context, cfg driver.Config, pw string) (driver.Conn, error) {
	d.mu.Lock()
	d.opens++
	d.passwords = append(d.passwords, pw)
	gate, err, delay := d.gate, d.openErr, d.pingDelay
	d.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	c := &fakeConn{delay: delay}
	d.mu.Lock()
	d.conns = append(d.conns, c)
	d.mu.Unlock()
	return c, nil
}

func (d *fakeDriver) set(f func(d *fakeDriver)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

func (d *fakeDriver) stats() (opens int, lastPassword string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.passwords) > 0 {
		lastPassword = d.passwords[len(d.passwords)-1]
	}
	return d.opens, lastPassword
}

func (d *fakeDriver) last() *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) == 0 {
		return nil
	}
	return d.conns[len(d.conns)-1]
}

type fakeConn struct {
	mu      sync.Mutex
	delay   time.Duration
	pingErr error
	closed  bool
}

func (c *fakeConn) Children(context.Context, *driver.Node) ([]*driver.Node, error) { return nil, nil }

func (c *fakeConn) NewSession(context.Context, driver.Scope) (driver.Session, error) {
	return nil, errors.New("no sessions")
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeConn) Ping(ctx context.Context) error {
	c.mu.Lock()
	delay, err, closed := c.delay, c.pingErr, c.closed
	c.mu.Unlock()
	time.Sleep(delay)
	if closed {
		return errors.New("connection is closed")
	}
	return err
}

func (c *fakeConn) set(delay time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delay, c.pingErr = delay, err
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func lookupOf(ds ...driver.Driver) func(driver.Kind) (driver.Driver, bool) {
	return func(k driver.Kind) (driver.Driver, bool) {
		for _, d := range ds {
			if d.Kind() == k {
				return d, true
			}
		}
		return nil, false
	}
}

func newTestManager(t *testing.T, secrets store.Secrets, opts ...Option) (*Manager, *fakeDriver, string) {
	t.Helper()
	d := &fakeDriver{kind: driver.Postgres, lang: driver.LangSQL}
	path := filepath.Join(t.TempDir(), "connections.toml")
	if secrets == nil {
		secrets = store.NewMemory()
	}
	m, err := New(path, secrets, append([]Option{WithLookup(lookupOf(d))}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, d, path
}

func pgConfig(name string) driver.Config {
	return driver.Config{Name: name, Kind: driver.Postgres, Host: "db.example"}
}

func ptr[T any](v T) *T { return &v }

func mustSave(t *testing.T, m *Manager, cfg driver.Config, pw *string) driver.Config {
	t.Helper()
	cfg, err := m.save(cfg, pw, false)
	if err != nil {
		t.Fatalf("save %q: %v", cfg.Name, err)
	}
	return cfg
}

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("error %v: code %v, want %v", err, got, code)
	}
}

func wantState(t *testing.T, m *Manager, id uuid.UUID, want quarryv1.ConnectionState) *quarryv1.ConnectionStatus {
	t.Helper()
	s := m.status(id)
	if s == nil || s.State != want {
		t.Fatalf("status of %s = %v, want %v", id, s, want)
	}
	return s
}

// collector records WatchStatus messages and folds them into a view.
type collector struct {
	mu   sync.Mutex
	msgs []*quarryv1.WatchStatusResponse
}

func (c *collector) send(msg *quarryv1.WatchStatusResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
	return nil
}

func (c *collector) view() map[string]quarryv1.ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := map[string]quarryv1.ConnectionState{}
	for _, msg := range c.msgs {
		for _, s := range msg.Statuses {
			v[s.Id] = s.State
		}
		for _, id := range msg.Deleted {
			delete(v, id)
		}
	}
	return v
}

func (c *collector) all() []*quarryv1.WatchStatusResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*quarryv1.WatchStatusResponse(nil), c.msgs...)
}

func startWatch(t *testing.T, m *Manager, send func(*quarryv1.WatchStatusResponse) error) (stop func() error) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.watch(ctx, send) }()
	return func() error {
		cancel()
		return <-done
	}
}

func TestSavePersists(t *testing.T) {
	m, _, path := newTestManager(t, nil)

	a := pgConfig("  Alpha ")
	a.Options = map[string]string{"sslmode": "disable"}
	a = mustSave(t, m, a, nil)
	if a.ID == uuid.Nil() || a.Name != "Alpha" || a.Port != 5432 {
		t.Fatalf("saved %+v: want an id, a trimmed name and the default port", a)
	}
	b := pgConfig("Beta")
	b.Port, b.Env, b.Group, b.Favorite, b.ReadOnly = 6000, "prod", "Work", true, true
	b = mustSave(t, m, b, nil)
	a.Name = "Alpha 2"
	if got := mustSave(t, m, a, nil); got.ID != a.ID || got.Name != "Alpha 2" {
		t.Fatalf("update = %+v", got)
	}
	wantState(t, m, a.ID, stDisconnected)

	m2, err := New(path, store.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if got, want := m2.list(), m.list(); len(got) != 2 || !reflect.DeepEqual(got[0].cfg, want[0].cfg) || !reflect.DeepEqual(got[1].cfg, want[1].cfg) {
		t.Fatalf("reloaded %+v, want %+v", got, want)
	}

	for _, tc := range []struct {
		name string
		cfg  driver.Config
		code connect.Code
	}{
		{"no name", pgConfig(" "), connect.CodeInvalidArgument},
		{"duplicate name", pgConfig("alpha 2"), connect.CodeInvalidArgument},
		{"bad env", driver.Config{Name: "x", Kind: driver.Postgres, Env: "staging"}, connect.CodeInvalidArgument},
		{"bad kind", driver.Config{Name: "x", Kind: "sqlite"}, connect.CodeInvalidArgument},
		{"unknown id", driver.Config{ID: uuid.New(), Name: "x", Kind: driver.Postgres}, connect.CodeNotFound},
	} {
		_, err := m.save(tc.cfg, nil, false)
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.code)
		}
	}
	if _, err := configOf(&quarryv1.ConnectionConfig{Id: "nope", Name: "x"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid id: %v", err)
	}
	if len(m.list()) != 2 {
		t.Errorf("failed saves changed the list: %+v", m.list())
	}
}

func TestPasswords(t *testing.T) {
	secrets := store.NewMemory()
	m, d, _ := newTestManager(t, secrets)
	ctx := t.Context()

	a := mustSave(t, m, pgConfig("A"), ptr("s3cret"))
	if !m.hasPassword(a.ID) {
		t.Fatal("password not stored")
	}
	mustSave(t, m, a, nil)
	if !m.hasPassword(a.ID) {
		t.Fatal("a save without password dropped it")
	}
	if _, err := m.connect(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, pw := d.stats(); pw != "s3cret" {
		t.Errorf("opened with %q, want the stored password", pw)
	}
	m.disconnect(a.ID)

	if _, err := m.save(a, nil, true); err != nil {
		t.Fatal(err)
	}
	if m.hasPassword(a.ID) {
		t.Fatal("forget_password kept the password")
	}
	// No stored password: try without one (trust auth, Redis without AUTH).
	if _, err := m.connect(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, pw := d.stats(); pw != "" {
		t.Errorf("opened with %q, want no password", pw)
	}

	b := pgConfig("B")
	b.AskPassword = true
	b = mustSave(t, m, b, ptr("ignored"))
	if m.hasPassword(b.ID) {
		t.Fatal("ask_password connection stored a password")
	}
	_, err := m.connect(ctx, b.ID, nil)
	wantCode(t, err, connect.CodeUnauthenticated)
	if !strings.Contains(err.Error(), "password required") {
		t.Errorf("message %q", err)
	}
	wantState(t, m, b.ID, stDisconnected)
	if _, err := m.connect(ctx, b.ID, ptr("typed")); err != nil {
		t.Fatal(err)
	}
	if _, pw := d.stats(); pw != "typed" {
		t.Errorf("opened with %q, want the given password", pw)
	}

	// Switching to ask_password drops a stored password.
	c := mustSave(t, m, pgConfig("C"), ptr("pw"))
	c.AskPassword = true
	mustSave(t, m, c, nil)
	if _, err := secrets.Get(c.ID); !errors.Is(err, store.ErrNoSecret) {
		t.Errorf("secret after ask_password: %v", err)
	}
}

// brokenSecrets behaves like a headless Linux box without a Secret Service.
type brokenSecrets struct{}

var errNoBus = fmt.Errorf("%w: no D-Bus session", store.ErrKeyringUnavailable)

func (brokenSecrets) Get(uuid.UUID) (string, error) { return "", errNoBus }
func (brokenSecrets) Set(uuid.UUID, string) error   { return errNoBus }
func (brokenSecrets) Delete(uuid.UUID) error        { return errNoBus }

// A keyring failure rolls the whole save back, so retrying never duplicates
// a connection; ask_password and password-less connections still save.
func TestKeyringUnavailable(t *testing.T) {
	m, _, path := newTestManager(t, brokenSecrets{})

	_, err := m.save(pgConfig("A"), ptr("pw"), false)
	wantCode(t, err, connect.CodeUnavailable)
	if !strings.Contains(err.Error(), "keyring unavailable") {
		t.Errorf("message %q", err)
	}
	if got := m.list(); len(got) != 0 {
		t.Fatalf("rolled back save is listed: %+v", got)
	}
	if got, _ := store.LoadConnections(path); len(got) != 0 {
		t.Fatalf("rolled back save is on disk: %+v", got)
	}

	a := mustSave(t, m, pgConfig("A"), nil)
	if m.hasPassword(a.ID) {
		t.Error("has_password with a broken keyring")
	}
	b := pgConfig("B")
	b.AskPassword = true
	mustSave(t, m, b, nil)

	renamed := a
	renamed.Name = "A2"
	_, err = m.save(renamed, ptr("pw"), false)
	wantCode(t, err, connect.CodeUnavailable)
	_, err = m.save(renamed, nil, true)
	wantCode(t, err, connect.CodeUnavailable)
	if got, _ := store.LoadConnections(path); len(got) != 2 || got[0].Name != "A" {
		t.Fatalf("on disk after failed updates: %+v", got)
	}
	if cfg, _ := m.Config(a.ID.String()); cfg.Name != "A" {
		t.Fatalf("in memory after failed updates: %+v", cfg)
	}

	// Without a keyring the stored password cannot be read: ask for it.
	_, err = m.connect(t.Context(), a.ID, nil)
	wantCode(t, err, connect.CodeUnauthenticated)
	if _, err := m.connect(t.Context(), a.ID, ptr("")); err != nil {
		t.Fatal(err)
	}
}

func TestConnectFailed(t *testing.T) {
	m, d, _ := newTestManager(t, nil)
	a := mustSave(t, m, pgConfig("A"), nil)

	d.set(func(d *fakeDriver) { d.openErr = errors.New(`password authentication failed for user "x"`) })
	_, err := m.connect(t.Context(), a.ID, nil)
	wantCode(t, err, connect.CodeUnavailable)
	if s := wantState(t, m, a.ID, stFailed); !strings.Contains(s.Error, "password authentication failed") {
		t.Errorf("status error %q", s.Error)
	}
	if _, _, err := m.Live(a.ID.String()); !errors.Is(err, service.ErrNotConnected) {
		t.Errorf("Live after a failed connect: %v", err)
	}

	d.set(func(d *fakeDriver) { d.openErr = nil })
	if s, err := m.connect(t.Context(), a.ID, nil); err != nil || s.State != stConnected || s.Error != "" {
		t.Fatalf("retry = %v, %v", s, err)
	}

	m.disconnect(a.ID)
	d.set(func(d *fakeDriver) { d.kind = driver.Redis })
	_, err = m.connect(t.Context(), a.ID, nil)
	wantCode(t, err, connect.CodeFailedPrecondition)
}

func TestConnectConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, d, _ := newTestManager(t, nil)
		a := mustSave(t, m, pgConfig("A"), nil)
		gate := make(chan struct{})
		d.set(func(d *fakeDriver) { d.gate = gate })

		var wg sync.WaitGroup
		results := make([]*quarryv1.ConnectionStatus, 5)
		for i := range results {
			wg.Go(func() {
				s, err := m.connect(t.Context(), a.ID, nil)
				if err != nil {
					t.Error(err)
				}
				results[i] = s
			})
		}
		synctest.Wait()
		wantState(t, m, a.ID, stConnecting)
		if _, _, err := m.Live(a.ID.String()); !errors.Is(err, service.ErrNotConnected) {
			t.Errorf("Live while connecting: %v", err)
		}
		close(gate)
		wg.Wait()
		for _, s := range results {
			if s == nil || s.State != stConnected {
				t.Errorf("result %v", s)
			}
		}
		if s, err := m.connect(t.Context(), a.ID, nil); err != nil || s.State != stConnected {
			t.Errorf("connect when connected = %v, %v", s, err)
		}
		if opens, _ := d.stats(); opens != 1 {
			t.Errorf("%d opens, want 1", opens)
		}
	})
}

func TestDisconnectWhileConnecting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, d, _ := newTestManager(t, nil)
		a := mustSave(t, m, pgConfig("A"), nil)
		d.set(func(d *fakeDriver) { d.gate = make(chan struct{}) })

		errc := make(chan error, 1)
		go func() {
			_, err := m.connect(t.Context(), a.ID, nil)
			errc <- err
		}()
		synctest.Wait()
		if err := m.disconnect(a.ID); err != nil {
			t.Fatal(err)
		}
		wantCode(t, <-errc, connect.CodeAborted)
		synctest.Wait()
		wantState(t, m, a.ID, stDisconnected)
		if d.last() != nil {
			t.Error("a cancelled open produced a connection")
		}
		// Disconnect is idempotent.
		if err := m.disconnect(a.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPingLostAndBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const every = 10 * time.Second
		m, d, _ := newTestManager(t, nil, WithPingInterval(every))
		a := mustSave(t, m, pgConfig("A"), nil)
		d.set(func(d *fakeDriver) { d.pingDelay = 3 * time.Millisecond })

		s, err := m.connect(t.Context(), a.ID, nil)
		if err != nil || s.State != stConnected || s.LatencyMs != 3 {
			t.Fatalf("connect = %v, %v; want CONNECTED with the first ping's latency", s, err)
		}
		var c collector
		stop := startWatch(t, m, c.send)

		conn := d.last()
		conn.set(time.Millisecond, errors.New("connection lost: EOF"))
		time.Sleep(every + time.Second)
		synctest.Wait()
		s = wantState(t, m, a.ID, stLost)
		if s.Error != "connection lost: EOF" || s.LatencyMs != 0 {
			t.Errorf("lost status %v", s)
		}
		// A lost connection stays usable: the driver reconnects on the next call.
		if got, _, err := m.Live(a.ID.String()); err != nil || got != conn {
			t.Errorf("Live while lost = %v, %v", got, err)
		}

		conn.set(5*time.Millisecond, nil)
		time.Sleep(every)
		synctest.Wait()
		if s = wantState(t, m, a.ID, stConnected); s.LatencyMs != 5 || s.Error != "" {
			t.Errorf("recovered status %v", s)
		}

		// An unchanged status is not broadcast again.
		time.Sleep(every)
		synctest.Wait()
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		var states []quarryv1.ConnectionState
		for _, msg := range c.all()[1:] {
			for _, s := range msg.Statuses {
				states = append(states, s.State)
			}
		}
		if want := []quarryv1.ConnectionState{stLost, stConnected}; !reflect.DeepEqual(states, want) {
			t.Errorf("watched %v, want %v", states, want)
		}

		m.disconnect(a.ID)
		if !conn.isClosed() {
			t.Error("disconnect did not close the connection")
		}
	})
}

func TestWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		a := mustSave(t, m, pgConfig("A"), nil)
		b := mustSave(t, m, pgConfig("B"), nil)

		var c collector
		stop := startWatch(t, m, c.send)
		synctest.Wait()
		first := c.all()[0]
		if len(first.Statuses) != 2 || len(first.Deleted) != 0 {
			t.Fatalf("first message %v", first)
		}

		if _, err := m.connect(t.Context(), a.ID, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.remove(b.ID); err != nil {
			t.Fatal(err)
		}
		cc := mustSave(t, m, pgConfig("C"), nil)
		synctest.Wait()
		want := map[string]quarryv1.ConnectionState{a.ID.String(): stConnected, cc.ID.String(): stDisconnected}
		if got := c.view(); !reflect.DeepEqual(got, want) {
			t.Errorf("view %v, want %v", got, want)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		n := len(m.watchers)
		m.mu.Unlock()
		if n != 0 {
			t.Errorf("%d watchers left after the stream ended", n)
		}
	})
}

func TestWatchSlowClientCoalesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		a := mustSave(t, m, pgConfig("A"), nil)

		release := make(chan struct{})
		var c collector
		stop := startWatch(t, m, func(msg *quarryv1.WatchStatusResponse) error {
			<-release
			return c.send(msg)
		})
		synctest.Wait() // stuck sending the first message

		for range 50 {
			if _, err := m.connect(t.Context(), a.ID, nil); err != nil {
				t.Fatal(err)
			}
			m.disconnect(a.ID)
		}
		b := mustSave(t, m, pgConfig("B"), nil)
		if err := m.remove(b.ID); err != nil {
			t.Fatal(err)
		}
		cc := mustSave(t, m, pgConfig("C"), nil)

		close(release)
		synctest.Wait()
		stop()
		msgs := c.all()
		if len(msgs) != 2 {
			t.Fatalf("%d messages, want the snapshot and one coalesced change: %v", len(msgs), msgs)
		}
		got := msgs[1]
		if len(got.Statuses) != 2 || len(got.Deleted) != 1 || got.Deleted[0] != b.ID.String() {
			t.Fatalf("coalesced message %v", got)
		}
		for _, s := range got.Statuses {
			if s.State != stDisconnected || (s.Id != a.ID.String() && s.Id != cc.ID.String()) {
				t.Errorf("coalesced status %v", s)
			}
		}
	})
}

func TestSaveDisconnectsOnTargetChange(t *testing.T) {
	m, d, _ := newTestManager(t, nil)
	ctx := t.Context()
	a := mustSave(t, m, pgConfig("A"), nil)
	if _, err := m.connect(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	conn := d.last()

	cosmetic := a
	cosmetic.Name, cosmetic.Env, cosmetic.Group, cosmetic.Favorite = "A renamed", "prod", "G", true
	mustSave(t, m, cosmetic, nil)
	wantState(t, m, a.ID, stConnected)
	got, cfg, err := m.Live(a.ID.String())
	if err != nil || got != conn || cfg.Name != "A renamed" || cfg.Env != "prod" {
		t.Fatalf("Live after a cosmetic change = %v, %+v, %v", got, cfg, err)
	}

	for _, change := range []func(*driver.Config){
		func(c *driver.Config) { c.Host = "other" },
		func(c *driver.Config) { c.Port = 5433 },
		func(c *driver.Config) { c.User = "bob" },
		func(c *driver.Config) { c.Database = "app" },
		func(c *driver.Config) { c.ReadOnly = !c.ReadOnly },
		func(c *driver.Config) { c.Options = map[string]string{"sslmode": "require"} },
	} {
		if _, err := m.connect(ctx, a.ID, nil); err != nil {
			t.Fatal(err)
		}
		conn := d.last()
		cfg, _ := m.Config(a.ID.String())
		change(&cfg)
		mustSave(t, m, cfg, nil)
		wantState(t, m, a.ID, stDisconnected)
		if !conn.isClosed() {
			t.Errorf("connection kept open after changing to %+v", cfg)
		}
	}
}

func TestDelete(t *testing.T) {
	secrets := store.NewMemory()
	m, d, path := newTestManager(t, secrets)
	a := mustSave(t, m, pgConfig("A"), ptr("pw"))
	keep := mustSave(t, m, pgConfig("Keep"), nil)
	if _, err := m.connect(t.Context(), a.ID, nil); err != nil {
		t.Fatal(err)
	}
	var c collector
	stop := startWatch(t, m, c.send)

	if err := m.remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if !d.last().isClosed() {
		t.Error("connection left open")
	}
	if _, err := secrets.Get(a.ID); !errors.Is(err, store.ErrNoSecret) {
		t.Errorf("secret after delete: %v", err)
	}
	_, _, err := m.Live(a.ID.String())
	wantCode(t, err, connect.CodeNotFound)
	if got, _ := store.LoadConnections(path); len(got) != 1 || got[0].ID != keep.ID {
		t.Errorf("on disk %+v", got)
	}
	wantCode(t, m.remove(a.ID), connect.CodeNotFound)

	deadline := time.Now().Add(5 * time.Second)
	for c.view()[a.ID.String()] != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	if v := c.view(); len(v) != 1 {
		t.Errorf("watcher view after delete %v", v)
	}
}

func TestLive(t *testing.T) {
	m, d, _ := newTestManager(t, nil)
	a := mustSave(t, m, pgConfig("A"), nil)

	_, _, err := m.Live("nope")
	wantCode(t, err, connect.CodeInvalidArgument)
	_, _, err = m.Live(uuid.New().String())
	wantCode(t, err, connect.CodeNotFound)
	_, cfg, err := m.Live(a.ID.String())
	if !errors.Is(err, service.ErrNotConnected) || cfg.ID != a.ID {
		t.Fatalf("Live before connect = %+v, %v", cfg, err)
	}
	wantCode(t, service.LiveError(err), connect.CodeFailedPrecondition)

	if _, err := m.connect(t.Context(), a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if c, _, err := m.Live(a.ID.String()); err != nil || c != d.last() {
		t.Fatalf("Live = %v, %v", c, err)
	}
	if _, ok := m.Config(a.ID.String()); !ok {
		t.Error("Config of a saved connection")
	}
	if _, ok := m.Config(uuid.New().String()); ok {
		t.Error("Config of an unknown connection")
	}
}

func TestClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, d, _ := newTestManager(t, nil, WithPingInterval(time.Second))
		a := mustSave(t, m, pgConfig("A"), nil)
		if _, err := m.connect(t.Context(), a.ID, nil); err != nil {
			t.Fatal(err)
		}
		var c collector
		done := make(chan error, 1)
		go func() { done <- m.watch(t.Context(), c.send) }()
		synctest.Wait()

		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Errorf("watch after Close: %v", err)
		}
		if !d.last().isClosed() {
			t.Error("Close left a connection open")
		}
		_, err := m.connect(t.Context(), a.ID, nil)
		wantCode(t, err, connect.CodeUnavailable)
		if err := m.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
}
