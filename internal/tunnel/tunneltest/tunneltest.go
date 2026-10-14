// Package tunneltest runs an in-process SSH server for tests: password and
// public key auth, and direct-tcpip channels forwarded with net.Dial.
package tunneltest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is an SSH server. Set the auth fields before serving.
type Server struct {
	HostKey       ssh.Signer
	Password      string        // accepted password; "" disables password auth
	AuthorizedKey ssh.PublicKey // accepted client key; nil disables key auth
	User          string        // required user; "" accepts any

	mu         sync.Mutex
	ignoreKA   bool
	keepalives int
	dials      []string
	conns      map[io.Closer]struct{}
	closed     bool
	wg         sync.WaitGroup
}

// NewServer returns a server with a fresh ed25519 host key. It does not
// listen; use Listen, or ServeConn over a net.Pipe. It is closed by
// t.Cleanup.
func NewServer(t testing.TB) *Server {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{HostKey: s, conns: map[io.Closer]struct{}{}}
	t.Cleanup(srv.Close)
	return srv
}

// Listen serves on a new 127.0.0.1 port and returns its address.
func (s *Server) Listen(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !s.track(ln) {
		t.Fatal("tunneltest: server is closed")
	}
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.ServeConn(c)
		}
	}()
	return ln.Addr().String()
}

// ServeConn runs one SSH connection until it ends.
func (s *Server) ServeConn(c net.Conn) {
	if !s.track(c) {
		c.Close()
		return
	}
	defer s.wg.Done()
	defer s.untrack(c)
	sc, chans, reqs, err := ssh.NewServerConn(c, s.config())
	if err != nil {
		return
	}
	defer sc.Close()
	go s.requests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		go s.direct(nc)
	}
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{}
	if s.Password != "" {
		cfg.PasswordCallback = func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if s.userOK(m) && string(pw) == s.Password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		}
	}
	if s.AuthorizedKey != nil {
		want := s.AuthorizedKey.Marshal()
		cfg.PublicKeyCallback = func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if s.userOK(m) && string(k.Marshal()) == string(want) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		}
	}
	cfg.AddHostKey(s.HostKey)
	return cfg
}

func (s *Server) userOK(m ssh.ConnMetadata) bool { return s.User == "" || m.User() == s.User }

func (s *Server) requests(reqs <-chan *ssh.Request) {
	for r := range reqs {
		s.mu.Lock()
		ignore := s.ignoreKA
		if r.Type == "keepalive@openssh.com" {
			s.keepalives++
		}
		s.mu.Unlock()
		if r.WantReply && !ignore {
			r.Reply(false, nil)
		}
	}
}

func (s *Server) direct(nc ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
		nc.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	addr := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	s.mu.Lock()
	s.dials = append(s.dials, addr)
	s.mu.Unlock()
	remote, err := net.Dial("tcp", addr)
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		remote.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	pipe(ch, remote)
}

// IgnoreKeepalives makes the server stop answering global requests, like a
// hung peer.
func (s *Server) IgnoreKeepalives() {
	s.mu.Lock()
	s.ignoreKA = true
	s.mu.Unlock()
}

// Keepalives is the number of keepalive requests received.
func (s *Server) Keepalives() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keepalives
}

// Dials lists the direct-tcpip targets requested, in order.
func (s *Server) Dials() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dials...)
}

// Close stops the listener and drops every connection.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) track(c io.Closer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	s.wg.Add(1)
	return true
}

func (s *Server) untrack(c io.Closer) {
	c.Close()
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

// Echo starts a TCP echo server on 127.0.0.1 and returns its address.
func Echo(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	})
	return ln.Addr().String()
}

// WriteKey writes a new ed25519 private key in OpenSSH format to dir,
// encrypted when passphrase is set, and returns its path and public key.
func WriteKey(t testing.TB, dir, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var b *pem.Block
	if passphrase == "" {
		b, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		b, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(b), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return path, k
}
