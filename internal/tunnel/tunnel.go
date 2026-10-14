package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Tunnel is a local port forward through SSH. When the SSH connection drops
// it is redialled on the next accepted connection, so a driver reconnecting
// to Addr gets a fresh path.
type Tunnel struct {
	cfg    Config
	target string
	auth   *auth
	ln     net.Listener
	ctx    context.Context // cancelled by Close; bounds redials
	cancel context.CancelFunc
	wg     sync.WaitGroup // accept loop and forwards

	dialMu sync.Mutex // one redial at a time

	mu     sync.Mutex
	chain  *chain
	conns  map[net.Conn]struct{}
	closed bool
}

// Open connects to the SSH server (through any jump hosts) and forwards
// every connection accepted on 127.0.0.1 to target, a "host:port" resolved by
// the SSH server. ctx bounds the initial connection only.
func Open(ctx context.Context, cfg Config, target string) (*Tunnel, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, fmt.Errorf("ssh: invalid forward target %q: %v", target, err)
	}
	c := cfg.withDefaults()
	a, err := loadAuth(&c)
	if err != nil {
		return nil, err
	}
	ch, err := connect(ctx, &c, a)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ch.close()
		return nil, fmt.Errorf("ssh: local listener: %w", err)
	}
	t := &Tunnel{cfg: c, target: target, auth: a, ln: ln, chain: ch, conns: map[net.Conn]struct{}{}}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.wg.Add(1)
	go t.serve()
	return t, nil
}

// Addr is the local address drivers connect to.
func (t *Tunnel) Addr() net.Addr { return t.ln.Addr() }

// Port is the local port of Addr.
func (t *Tunnel) Port() int { return t.ln.Addr().(*net.TCPAddr).Port }

// Close stops listening, drops the forwarded connections and closes the SSH
// clients.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conns := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	ch := t.chain
	t.mu.Unlock()

	t.cancel()
	err := t.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	if ch != nil {
		ch.close()
	}
	t.wg.Wait()
	return err
}

func (t *Tunnel) serve() {
	defer t.wg.Done()
	for {
		c, err := t.ln.Accept()
		if err != nil {
			return
		}
		if !t.track(c) {
			c.Close()
			return
		}
		t.wg.Add(1)
		go t.forward(c)
	}
}

func (t *Tunnel) forward(local net.Conn) {
	defer t.wg.Done()
	defer t.untrack(local)
	remote, err := t.dialTarget()
	if err != nil {
		return
	}
	if !t.track(remote) {
		remote.Close()
		return
	}
	defer t.untrack(remote)

	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()
	<-done
	local.Close()
	remote.Close()
	<-done
}

// dialTarget opens a direct-tcpip channel to the target, redialling SSH once
// when the current connection turns out to be dead.
func (t *Tunnel) dialTarget() (net.Conn, error) {
	for try := 0; ; try++ {
		ch, err := t.current()
		if err != nil {
			return nil, err
		}
		c, err := ch.client().DialContext(t.ctx, "tcp", t.target)
		var oe *ssh.OpenChannelError
		if err == nil || try > 0 || errors.As(err, &oe) || t.ctx.Err() != nil {
			return c, err
		}
		ch.close()
	}
}

// current returns the live SSH chain, dialling a new one when needed.
func (t *Tunnel) current() (*chain, error) {
	t.dialMu.Lock()
	defer t.dialMu.Unlock()
	t.mu.Lock()
	ch := t.chain
	t.mu.Unlock()
	if ch != nil && ch.alive() {
		return ch, nil
	}
	ch, err := connect(t.ctx, &t.cfg, t.auth)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		ch.close()
		return nil, net.ErrClosed
	}
	t.chain = ch
	return ch, nil
}

func (t *Tunnel) track(c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.conns[c] = struct{}{}
	return true
}

func (t *Tunnel) untrack(c net.Conn) {
	c.Close()
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
}

// chain is one SSH connection: the jump host clients followed by the server.
type chain struct {
	clients []*ssh.Client
	done    chan struct{}
	once    sync.Once
}

func (c *chain) client() *ssh.Client { return c.clients[len(c.clients)-1] }

func (c *chain) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *chain) close() {
	c.once.Do(func() {
		close(c.done)
		for _, cl := range slices.Backward(c.clients) {
			cl.Close()
		}
	})
}

// keepalive closes the chain when the server stops answering keepalives.
func (c *chain) keepalive(every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
		}
		res := make(chan error, 1)
		go func() {
			_, _, err := c.client().SendRequest("keepalive@openssh.com", true, nil)
			res <- err
		}()
		timeout := time.NewTimer(every)
		select {
		case err := <-res:
			timeout.Stop()
			if err != nil {
				c.close()
				return
			}
		case <-timeout.C:
			c.close()
			return
		case <-c.done:
			timeout.Stop()
			return
		}
	}
}

// connect dials the jump hosts and the server in turn, each through the
// previous one.
func connect(ctx context.Context, cfg *Config, a *auth) (*chain, error) {
	hops, err := cfg.hops()
	if err != nil {
		return nil, err
	}
	ch := &chain{done: make(chan struct{})}
	for i, h := range hops {
		var conn net.Conn
		if i == 0 {
			conn, err = cfg.dial(ctx, "tcp", h.addr())
		} else {
			conn, err = ch.client().DialContext(ctx, "tcp", h.addr())
		}
		if err != nil {
			ch.close()
			return nil, fmt.Errorf("ssh: connecting to %s: %w", h.addr(), err)
		}
		cl, err := handshake(ctx, conn, h, cfg, a)
		if err != nil {
			ch.close()
			return nil, err
		}
		ch.clients = append(ch.clients, cl)
	}
	for _, cl := range ch.clients {
		go func() {
			cl.Wait()
			ch.close()
		}()
	}
	if cfg.KeepAlive > 0 {
		go ch.keepalive(cfg.KeepAlive)
	}
	return ch, nil
}

// handshake runs the SSH handshake over conn, bounded by ctx and
// cfg.Timeout; the timeout is paused while Accept decides on a new host key.
func handshake(ctx context.Context, conn net.Conn, h hop, cfg *Config, a *auth) (*ssh.Client, error) {
	timer := time.AfterFunc(cfg.Timeout, func() { conn.Close() })
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	v := &verifier{
		path:   cfg.KnownHosts,
		accept: cfg.Accept,
		pause:  func() { timer.Stop() },
		resume: func() { timer.Reset(cfg.Timeout) },
	}
	methods, tried, release := a.methods()
	defer release()
	cc := &ssh.ClientConfig{
		User:              h.user,
		Auth:              methods,
		HostKeyCallback:   v.check,
		HostKeyAlgorithms: knownAlgorithms(cfg.KnownHosts, h.addr()),
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, h.addr(), cc)
	timedOut := !timer.Stop()
	cancelled := !stop()
	if err == nil && !timedOut && !cancelled {
		return ssh.NewClient(sc, chans, reqs), nil
	}
	if sc != nil {
		sc.Close()
	}
	conn.Close()
	switch {
	case cancelled:
		return nil, fmt.Errorf("ssh: connecting to %s: %w", h.addr(), context.Cause(ctx))
	case v.err != nil:
		return nil, v.err
	case timedOut:
		return nil, fmt.Errorf("ssh: %s: handshake timed out after %s", h.addr(), cfg.Timeout)
	case isAuthErr(err):
		return nil, authError(h, tried)
	default:
		return nil, fmt.Errorf("ssh: %s: %w", h.addr(), err)
	}
}
