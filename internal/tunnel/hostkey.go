package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var (
	// ErrHostKeyUnknown means the host is not in known_hosts and was not
	// accepted.
	ErrHostKeyUnknown = errors.New("ssh: unknown host key")
	// ErrHostKeyMismatch means known_hosts lists a different key for the
	// host.
	ErrHostKeyMismatch = errors.New("ssh: host key mismatch")
)

// knownHostsMu serialises reads and appends of known_hosts files.
var knownHostsMu sync.Mutex

func loadKnownHosts(path string) (ssh.HostKeyCallback, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return knownhosts.New()
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: reading %s: %v", tildePath(path), err)
	}
	return cb, nil
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ssh: saving host key: %w", err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("ssh: saving host key: %w", err)
	}
	_, err = f.WriteString(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("ssh: saving host key: %w", err)
	}
	return nil
}

// verifier checks host keys against known_hosts, asking accept for unknown
// hosts. pause/resume stop the handshake timeout while the user decides.
type verifier struct {
	path          string
	accept        func(string, net.Addr, ssh.PublicKey) bool
	pause, resume func()
	err           error // the last rejection, reported instead of the handshake error
}

func (v *verifier) check(hostname string, remote net.Addr, key ssh.PublicKey) error {
	v.err = v.verify(hostname, remote, key)
	return v.err
}

func (v *verifier) verify(hostname string, remote net.Addr, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	db, err := loadKnownHosts(v.path)
	knownHostsMu.Unlock()
	if err != nil {
		return err
	}
	err = db(hostname, remote, key)
	var ke *knownhosts.KeyError
	var re *knownhosts.RevokedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &re):
		return fmt.Errorf("ssh: host key for %s is revoked in %s", hostname, tildePath(re.Revoked.Filename))
	case errors.As(err, &ke) && len(ke.Want) > 0:
		w := ke.Want[0]
		return fmt.Errorf("%w for %s — possible MITM; fix %s (line %d)", ErrHostKeyMismatch, hostname, tildePath(w.Filename), w.Line)
	case errors.As(err, &ke):
		fp := ssh.FingerprintSHA256(key)
		if v.accept == nil {
			return fmt.Errorf("%w for %s (%s %s); it is not in %s", ErrHostKeyUnknown, hostname, key.Type(), fp, tildePath(v.path))
		}
		v.pause()
		ok := v.accept(hostname, remote, key)
		v.resume()
		if !ok {
			return fmt.Errorf("%w for %s (%s %s): not trusted", ErrHostKeyUnknown, hostname, key.Type(), fp)
		}
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()
		return appendKnownHost(v.path, hostname, key)
	default:
		return fmt.Errorf("ssh: host key for %s: %v", hostname, err)
	}
}

var probeKey = sync.OnceValue(func() ssh.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(pub)
	return k
})

// knownAlgorithms returns the host key algorithms matching the keys
// known_hosts has for hostname, so the server presents one we can verify
// instead of an unknown type that would look like a mismatch. It returns nil
// for unknown hosts.
func knownAlgorithms(path, hostname string) []string {
	knownHostsMu.Lock()
	db, err := loadKnownHosts(path)
	knownHostsMu.Unlock()
	if err != nil {
		return nil
	}
	var ke *knownhosts.KeyError
	if err := db(hostname, &net.TCPAddr{IP: net.IPv4zero}, probeKey()); !errors.As(err, &ke) {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	for _, w := range ke.Want {
		t := w.Key.Type()
		if seen[t] {
			continue
		}
		seen[t] = true
		if t == ssh.KeyAlgoRSA {
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		}
		algos = append(algos, t)
	}
	return algos
}
