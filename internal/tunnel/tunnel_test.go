package tunnel

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
	"testing/synctest"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/tunnel/tunneltest"
)

func TestMain(m *testing.M) {
	// Keep the user's agent and ~/.ssh keys out of the tests.
	agentDial = func() (io.ReadWriteCloser, error) { return nil, errors.New("no agent in tests") }
	home, err := os.MkdirTemp("", "tunnel-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// trust writes a known_hosts file listing s for addr.
func trust(t *testing.T, path, addr string, keys ...ssh.PublicKey) {
	t.Helper()
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(knownhosts.Line([]string{knownhosts.Normalize(addr)}, k) + "\n")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
}

func serverConfig(t *testing.T, addr string) Config {
	t.Helper()
	h, p := hostPort(t, addr)
	return Config{Host: h, Port: p, User: "alice", KnownHosts: filepath.Join(t.TempDir(), "known_hosts"), Timeout: 5 * time.Second}
}

func openTunnel(t *testing.T, cfg Config, target string) *Tunnel {
	t.Helper()
	tun, err := Open(context.Background(), cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tun.Close() })
	return tun
}

func echo(t *testing.T, addr, msg string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
}

func TestPasswordAuth(t *testing.T) {
	s := tunneltest.NewServer(t)
	s.Password, s.User = "secret", "alice"
	addr := s.Listen(t)
	target := tunneltest.Echo(t)

	cfg := serverConfig(t, addr)
	cfg.Password = "secret"
	trust(t, cfg.KnownHosts, addr, s.HostKey.PublicKey())
	tun := openTunnel(t, cfg, target)

	if a := tun.Addr().(*net.TCPAddr); !a.IP.IsLoopback() || a.Port != tun.Port() || a.Port == 0 {
		t.Fatalf("Addr = %v, Port = %d", a, tun.Port())
	}
	echo(t, tun.Addr().String(), "hello")
	echo(t, tun.Addr().String(), "again")
	if got := s.Dials(); !slices.Equal(got, []string{target, target}) {
		t.Fatalf("server dials = %v, want two to %s", got, target)
	}
}

func TestKeyAuth(t *testing.T) {
	for _, pass := range []string{"", "p4ss"} {
		t.Run("passphrase="+pass, func(t *testing.T) {
			key, pub := tunneltest.WriteKey(t, t.TempDir(), pass)
			s := tunneltest.NewServer(t)
			s.AuthorizedKey = pub
			addr := s.Listen(t)

			cfg := serverConfig(t, addr)
			cfg.KeyFile = key
			trust(t, cfg.KnownHosts, addr, s.HostKey.PublicKey())
			if pass != "" {
				if _, err := Open(context.Background(), cfg, tunneltest.Echo(t)); !errors.Is(err, ErrPassphrase) {
					t.Fatalf("Open without passphrase: %v, want ErrPassphrase", err)
				}
				cfg.Passphrase = pass
			}
			tun := openTunnel(t, cfg, tunneltest.Echo(t))
			echo(t, tun.Addr().String(), "key")
		})
	}
}

func TestWrongCredentials(t *testing.T) {
	_, pub := tunneltest.WriteKey(t, t.TempDir(), "")
	other, _ := tunneltest.WriteKey(t, t.TempDir(), "")
	s := tunneltest.NewServer(t)
	s.Password, s.AuthorizedKey = "secret", pub
	addr := s.Listen(t)

	for name, set := range map[string]func(*Config){
		"password": func(c *Config) { c.Password = "nope" },
		"key":      func(c *Config) { c.KeyFile = other },
		"nothing":  func(*Config) {},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := serverConfig(t, addr)
			trust(t, cfg.KnownHosts, addr, s.HostKey.PublicKey())
			set(&cfg)
			_, err := Open(context.Background(), cfg, "db:5432")
			if !errors.Is(err, ErrAuth) || !errors.Is(err, driver.ErrAuth) {
				t.Fatalf("Open: %v, want ErrAuth wrapping driver.ErrAuth", err)
			}
			if !strings.Contains(err.Error(), "alice@") {
				t.Errorf("error %q does not name the user", err)
			}
		})
	}
}

func TestUnknownHost(t *testing.T) {
	s := tunneltest.NewServer(t)
	s.Password = "secret"
	addr := s.Listen(t)
	target := tunneltest.Echo(t)
	fp := ssh.FingerprintSHA256(s.HostKey.PublicKey())

	cfg := serverConfig(t, addr)
	cfg.Password = "secret"

	t.Run("rejected", func(t *testing.T) {
		_, err := Open(context.Background(), cfg, target)
		if !errors.Is(err, ErrHostKeyUnknown) || !strings.Contains(err.Error(), fp) {
			t.Fatalf("Open: %v, want ErrHostKeyUnknown with %s", err, fp)
		}
		c := cfg
		asked := 0
		c.Accept = func(host string, _ net.Addr, k ssh.PublicKey) bool {
			asked++
			return false
		}
		if _, err := Open(context.Background(), c, target); !errors.Is(err, ErrHostKeyUnknown) || asked != 1 {
			t.Fatalf("Open with refusing Accept: %v (asked %d)", err, asked)
		}
		if _, err := os.Stat(cfg.KnownHosts); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("known_hosts written after a refusal: %v", err)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		c := cfg
		c.Accept = func(host string, _ net.Addr, k ssh.PublicKey) bool {
			return host == addr && ssh.FingerprintSHA256(k) == fp
		}
		echo(t, openTunnel(t, c, target).Addr().String(), "tofu")

		b, err := os.ReadFile(cfg.KnownHosts)
		if err != nil || !strings.Contains(string(b), knownhosts.Normalize(addr)) {
			t.Fatalf("known_hosts = %q, %v", b, err)
		}
		// Now known: no Accept needed.
		echo(t, openTunnel(t, cfg, target).Addr().String(), "known")
	})
}

func TestChangedHostKey(t *testing.T) {
	s := tunneltest.NewServer(t)
	s.Password = "secret"
	addr := s.Listen(t)
	old := tunneltest.NewServer(t).HostKey.PublicKey()

	cfg := serverConfig(t, addr)
	cfg.Password = "secret"
	trust(t, cfg.KnownHosts, addr, old)
	cfg.Accept = func(string, net.Addr, ssh.PublicKey) bool {
		t.Error("Accept called for a changed key")
		return true
	}
	_, err := Open(context.Background(), cfg, "db:5432")
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("Open: %v, want ErrHostKeyMismatch", err)
	}
}

func TestJumpHosts(t *testing.T) {
	_, pub := tunneltest.WriteKey(t, t.TempDir(), "")
	jump1, jump2, final := tunneltest.NewServer(t), tunneltest.NewServer(t), tunneltest.NewServer(t)
	jump1.Password, jump1.User = "secret", "bob"
	jump2.Password, jump2.AuthorizedKey = "secret", pub
	final.Password, final.User = "secret", "alice"
	j1, j2, fa := jump1.Listen(t), jump2.Listen(t), final.Listen(t)
	target := tunneltest.Echo(t)

	cfg := serverConfig(t, fa)
	cfg.Password = "secret"
	cfg.Jump = []string{"bob@" + j1, j2}
	trust(t, cfg.KnownHosts, j1, jump1.HostKey.PublicKey())
	trust(t, cfg.KnownHosts, j2, jump2.HostKey.PublicKey())
	trust(t, cfg.KnownHosts, fa, final.HostKey.PublicKey())

	echo(t, openTunnel(t, cfg, target).Addr().String(), "hop")
	if got := jump1.Dials(); !slices.Equal(got, []string{j2}) {
		t.Errorf("jump1 dials = %v, want %s", got, j2)
	}
	if got := jump2.Dials(); !slices.Equal(got, []string{fa}) {
		t.Errorf("jump2 dials = %v, want %s", got, fa)
	}
	if got := final.Dials(); !slices.Equal(got, []string{target}) {
		t.Errorf("final dials = %v, want %s", got, target)
	}
}

func TestClose(t *testing.T) {
	s := tunneltest.NewServer(t)
	s.Password = "secret"
	addr := s.Listen(t)

	cfg := serverConfig(t, addr)
	cfg.Password = "secret"
	trust(t, cfg.KnownHosts, addr, s.HostKey.PublicKey())
	tun := openTunnel(t, cfg, tunneltest.Echo(t))
	local := tun.Addr().String()

	c, err := net.Dial("tcp", local)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "x")
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("forwarded connection still open after Close")
	}
	if c, err := net.DialTimeout("tcp", local, time.Second); err == nil {
		c.Close()
		t.Fatal("still listening after Close")
	}
}

func TestRedialAfterDrop(t *testing.T) {
	s := tunneltest.NewServer(t)
	s.Password = "secret"
	addr := s.Listen(t)

	cfg := serverConfig(t, addr)
	cfg.Password = "secret"
	trust(t, cfg.KnownHosts, addr, s.HostKey.PublicKey())
	tun := openTunnel(t, cfg, tunneltest.Echo(t))

	tun.mu.Lock()
	first := tun.chain
	tun.mu.Unlock()
	first.client().Close()
	<-first.done
	echo(t, tun.Addr().String(), "back")
}

func TestKeepAlive(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	synctest.Test(t, func(t *testing.T) {
		s := tunneltest.NewServer(t)
		s.Password = "secret"
		trust(t, kh, "bastion:22", s.HostKey.PublicKey())
		cfg := Config{Host: "bastion", User: "alice", Password: "secret", KnownHosts: kh, KeepAlive: 30 * time.Second}
		cfg.dial = func(context.Context, string, string) (net.Conn, error) {
			c1, c2 := bufPipe()
			go s.ServeConn(c2)
			return c1, nil
		}
		c := cfg.withDefaults()
		a, err := loadAuth(&c)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := connect(t.Context(), &c, a)
		if err != nil {
			t.Fatal(err)
		}
		defer ch.close()

		time.Sleep(95 * time.Second)
		synctest.Wait()
		if n := s.Keepalives(); n != 3 || !ch.alive() {
			t.Fatalf("after 95s: %d keepalives, alive %v; want 3, true", n, ch.alive())
		}

		s.IgnoreKeepalives()
		time.Sleep(54 * time.Second) // next keepalive at 120s, times out at 150s
		synctest.Wait()
		if !ch.alive() {
			t.Fatal("closed before the keepalive timed out")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if ch.alive() {
			t.Fatal("still alive after an unanswered keepalive")
		}
	})
}

func TestFromOptions(t *testing.T) {
	if _, ok := FromOptions(map[string]string{"ssh_user": "x"}); ok {
		t.Fatal("ok without ssh_host")
	}
	cfg, ok := FromOptions(map[string]string{
		"ssh_host": " bastion ", "ssh_port": "2222", "ssh_user": "alice", "ssh_key": "~/.ssh/k",
		"ssh_known_hosts": "/kh", "ssh_jump": "a@j1:22, j2", "ssh_agent": "true",
	})
	if !ok || cfg.Host != "bastion" || cfg.Port != 2222 || cfg.User != "alice" || cfg.KeyFile != "~/.ssh/k" ||
		cfg.KnownHosts != "/kh" || !slices.Equal(cfg.Jump, []string{"a@j1:22", "j2"}) || !cfg.UseAgent || cfg.Accept != nil {
		t.Fatalf("FromOptions = %+v, %v", cfg, ok)
	}
	if cfg, _ := FromOptions(map[string]string{"ssh_host": "h", "ssh_accept_new": "true"}); cfg.Accept == nil {
		t.Fatal("ssh_accept_new did not set Accept")
	}
	cfg, _ = FromOptions(map[string]string{"ssh_host": "h", "ssh_port": "x"})
	if _, err := Open(context.Background(), cfg, "db:5432"); err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("Open with a bad port: %v", err)
	}
	cfg, _ = FromOptions(map[string]string{"ssh_host": "h", "ssh_jump": "@:x"})
	if _, err := Open(context.Background(), cfg, "db:5432"); err == nil || !strings.Contains(err.Error(), "invalid jump host") {
		t.Fatalf("Open with a bad jump host: %v", err)
	}
}

// bufPipe is a net.Pipe whose writes do not wait for the reader, like TCP:
// SSH peers both write their version line before reading. It blocks only on
// channels, so it works inside a synctest bubble.
func bufPipe() (net.Conn, net.Conn) {
	ab, ba := make(chan []byte, 1024), make(chan []byte, 1024)
	done, once := make(chan struct{}), new(sync.Once)
	return &pipeEnd{in: ba, out: ab, done: done, once: once}, &pipeEnd{in: ab, out: ba, done: done, once: once}
}

type pipeEnd struct {
	in, out chan []byte
	buf     []byte
	done    chan struct{}
	once    *sync.Once
}

func (p *pipeEnd) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		select {
		case p.buf = <-p.in:
		case <-p.done:
			return 0, io.EOF
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *pipeEnd) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, net.ErrClosed
	default:
	}
	select {
	case p.out <- slices.Clone(b):
		return len(b), nil
	case <-p.done:
		return 0, net.ErrClosed
	}
}

func (p *pipeEnd) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *pipeEnd) LocalAddr() net.Addr              { return pipeAddr }
func (p *pipeEnd) RemoteAddr() net.Addr             { return pipeAddr }
func (p *pipeEnd) SetDeadline(time.Time) error      { return nil }
func (p *pipeEnd) SetReadDeadline(time.Time) error  { return nil }
func (p *pipeEnd) SetWriteDeadline(time.Time) error { return nil }

// pipeAddr is a TCP address because knownhosts parses it.
var pipeAddr = &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 22}
