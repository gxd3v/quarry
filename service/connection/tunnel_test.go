package connection

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"connectrpc.com/connect"
	"golang.org/x/crypto/ssh"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/internal/tunnel/tunneltest"
)

// echoDriver checks that the address it is given echoes, and keeps that
// connection open until the Conn is closed.
type echoDriver struct {
	mu    sync.Mutex
	addrs []string
}

func (d *echoDriver) Kind() driver.Kind         { return driver.Postgres }
func (d *echoDriver) Language() driver.Language { return driver.LangSQL }

func (d *echoDriver) Open(ctx context.Context, cfg driver.Config, _ string) (driver.Conn, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	d.mu.Lock()
	d.addrs = append(d.addrs, addr)
	d.mu.Unlock()
	var dl net.Dialer
	c, err := dl.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 4)
	if _, err := io.WriteString(c, "ping"); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		c.Close()
		return nil, errors.New("no echo through the tunnel")
	}
	return &echoConn{c: c}, nil
}

func (d *echoDriver) opened() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.addrs)
}

type echoConn struct {
	fakeConn
	c net.Conn
}

func (c *echoConn) Close() error { return c.c.Close() }

type sshEnv struct {
	m       *Manager
	d       *echoDriver
	srv     *tunneltest.Server
	secrets store.Secrets
	echo    string
	kh      string
	cfg     driver.Config
}

// newSSHEnv sets up a manager and a saved connection whose database (an echo
// server) is reached through an in-process SSH server. ssh_key is always set,
// so the user's agent and keys stay unused; the server accepts it only when
// keyAuth is set.
func newSSHEnv(t *testing.T, keyAuth bool, opts map[string]string) *sshEnv {
	t.Helper()
	dir := t.TempDir()
	e := &sshEnv{d: &echoDriver{}, secrets: store.NewMemory(), echo: tunneltest.Echo(t), kh: filepath.Join(dir, "known_hosts")}
	e.srv = tunneltest.NewServer(t)
	e.srv.Password, e.srv.User = "ssh-secret", "alice"
	key, pub := tunneltest.WriteKey(t, dir, "")
	if keyAuth {
		e.srv.AuthorizedKey = pub
	}
	sh, sp, _ := net.SplitHostPort(e.srv.Listen(t))

	var err error
	e.m, err = New(filepath.Join(dir, "connections.toml"), e.secrets, WithLookup(lookupOf(e.d)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.m.Close() })

	h, p, _ := net.SplitHostPort(e.echo)
	port, _ := strconv.Atoi(p)
	cfg := driver.Config{Name: "db", Kind: driver.Postgres, Host: h, Port: port, Options: map[string]string{
		"ssh_host": sh, "ssh_port": sp, "ssh_user": "alice", "ssh_key": key, "ssh_known_hosts": e.kh,
	}}
	for k, v := range opts {
		cfg.Options[k] = v
	}
	e.cfg = mustSave(t, e.m, cfg, nil)
	return e
}

func (e *sshEnv) setSSHPassword(t *testing.T, pw string) {
	t.Helper()
	if err := e.secrets.Set(sshSecretID(e.cfg.ID, sshPassword), pw); err != nil {
		t.Fatal(err)
	}
}

func TestConnectThroughSSHTunnel(t *testing.T) {
	e := newSSHEnv(t, false, map[string]string{"ssh_accept_new": "true"})
	e.setSSHPassword(t, "ssh-secret")

	if _, err := e.m.connect(t.Context(), e.cfg.ID, nil); err != nil {
		t.Fatal(err)
	}
	wantState(t, e.m, e.cfg.ID, stConnected)
	opened := e.d.opened()
	if len(opened) != 1 || opened[0] == e.echo || !strings.HasPrefix(opened[0], "127.0.0.1:") {
		t.Fatalf("driver opened %v, want the tunnel's local address", opened)
	}
	if got := e.srv.Dials(); !slices.Equal(got, []string{e.echo}) {
		t.Fatalf("SSH server forwarded to %v, want %s", got, e.echo)
	}
	if b, err := os.ReadFile(e.kh); err != nil || len(b) == 0 {
		t.Fatalf("host key not saved with ssh_accept_new: %q, %v", b, err)
	}
	if cfg, _ := e.m.Config(e.cfg.ID.String()); cfg.Host != e.cfg.Host || cfg.Port != e.cfg.Port {
		t.Fatalf("saved config rewritten to %s:%d", cfg.Host, cfg.Port)
	}

	if err := e.m.disconnect(e.cfg.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", opened[0], time.Second); err == nil {
		c.Close()
		t.Fatal("tunnel still listening after disconnect")
	}

	if err := e.m.remove(e.cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.secrets.Get(sshSecretID(e.cfg.ID, sshPassword)); !errors.Is(err, store.ErrNoSecret) {
		t.Fatalf("SSH password kept after delete: %v", err)
	}
}

func TestSSHUnknownHost(t *testing.T) {
	e := newSSHEnv(t, false, nil)
	e.setSSHPassword(t, "ssh-secret")

	_, err := e.m.connect(t.Context(), e.cfg.ID, nil)
	fp := ssh.FingerprintSHA256(e.srv.HostKey.PublicKey())
	if err == nil || !strings.Contains(err.Error(), fp) || !strings.Contains(err.Error(), "ssh_accept_new") {
		t.Fatalf("connect: %v, want an error with %s and the ssh_accept_new hint", err, fp)
	}
	if s := wantState(t, e.m, e.cfg.ID, stFailed); !strings.Contains(s.Error, fp) {
		t.Fatalf("status error %q lacks the fingerprint", s.Error)
	}
	if len(e.d.opened()) != 0 {
		t.Fatal("driver opened without a tunnel")
	}
	if _, err := os.Stat(e.kh); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("known_hosts written without ssh_accept_new: %v", err)
	}
}

func TestSSHWrongPassword(t *testing.T) {
	e := newSSHEnv(t, false, map[string]string{"ssh_accept_new": "true"})
	e.setSSHPassword(t, "wrong")

	_, err := e.m.connect(t.Context(), e.cfg.ID, nil)
	wantCode(t, err, connect.CodePermissionDenied)
	if !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("error %q does not say SSH failed", err)
	}
	if len(e.d.opened()) != 0 {
		t.Fatal("driver opened after SSH auth failed")
	}
}

func TestTestConnectionThroughSSHKey(t *testing.T) {
	e := newSSHEnv(t, true, map[string]string{"ssh_accept_new": "true"})

	cfg := e.cfg
	cfg.ID = uuid.Nil()
	if _, err := e.m.test(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	opened := e.d.opened()
	if len(opened) != 1 {
		t.Fatalf("driver opened %v", opened)
	}
	if c, err := net.DialTimeout("tcp", opened[0], time.Second); err == nil {
		c.Close()
		t.Fatal("tunnel still listening after the test connection")
	}
}

func TestSSHSecretID(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	ids := []any{sshSecretID(a, sshPassword), sshSecretID(a, sshPassphrase), sshSecretID(b, sshPassword), a, b}
	for i := range ids {
		for j := range i {
			if ids[i] == ids[j] {
				t.Fatalf("ids %d and %d collide: %v", i, j, ids[i])
			}
		}
	}
	if sshSecretID(a, sshPassword) != sshSecretID(a, sshPassword) {
		t.Fatal("sshSecretID is not stable")
	}
}
