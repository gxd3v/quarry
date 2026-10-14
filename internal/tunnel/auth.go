package tunnel

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/gxd3v/quarry/internal/driver"
)

var (
	// ErrAuth means every authentication method was refused. It wraps
	// driver.ErrAuth.
	ErrAuth = fmt.Errorf("ssh: %w", driver.ErrAuth)
	// ErrPassphrase means KeyFile is encrypted and Passphrase is missing or
	// wrong; the UI may prompt and retry.
	ErrPassphrase = errors.New("ssh: key passphrase required")
)

const windowsAgentPipe = `\\.\pipe\openssh-ssh-agent`

// agentDial is replaced in tests so they never touch the user's agent.
var agentDial = dialAgent

type namedSigner struct {
	name   string
	signer ssh.Signer
}

// auth holds the key material loaded once per Open; the agent is dialed
// per handshake.
type auth struct {
	keys       []namedSigner
	skipped    []string // default keys that could not be used, for messages
	useAgent   bool
	agentFirst bool
	password   string
}

func loadAuth(cfg *Config) (*auth, error) {
	a := &auth{password: cfg.Password}
	if cfg.KeyFile != "" {
		s, err := loadKey(cfg.KeyFile, cfg.Passphrase)
		if err != nil {
			return nil, err
		}
		a.keys = []namedSigner{{tildePath(cfg.KeyFile), s}}
		a.useAgent = cfg.UseAgent
		return a, nil
	}
	a.useAgent, a.agentFirst = true, true
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		path := expandHome("~/.ssh/" + name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		s, err := loadKey(path, cfg.Passphrase)
		switch {
		case errors.Is(err, ErrPassphrase):
			a.skipped = append(a.skipped, tildePath(path)+" (encrypted)")
		case err != nil:
			a.skipped = append(a.skipped, tildePath(path)+" (unreadable)")
		default:
			a.keys = append(a.keys, namedSigner{tildePath(path), s})
		}
	}
	return a, nil
}

func loadKey(path, passphrase string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("ssh: key file %s not found", tildePath(path))
		}
		return nil, fmt.Errorf("ssh: reading key %s: %w", tildePath(path), err)
	}
	s, err := ssh.ParsePrivateKey(b)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == "" {
			return nil, fmt.Errorf("%w for %s", ErrPassphrase, tildePath(path))
		}
		s, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(passphrase))
		if errors.Is(err, x509.IncorrectPasswordError) {
			return nil, fmt.Errorf("%w for %s: wrong passphrase", ErrPassphrase, tildePath(path))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("ssh: cannot parse key %s: %v", tildePath(path), err)
	}
	return s, nil
}

// methods returns the auth methods for one handshake, the names of the
// sources offered (for error messages) and a func releasing the agent.
func (a *auth) methods() ([]ssh.AuthMethod, []string, func()) {
	var (
		signers []ssh.Signer
		tried   []string
		release = func() {}
	)
	addKeys := func() {
		for _, k := range a.keys {
			signers = append(signers, k.signer)
			tried = append(tried, k.name)
		}
	}
	addAgent := func() {
		if !a.useAgent {
			return
		}
		conn, err := agentDial()
		if err != nil {
			return
		}
		ss, err := agent.NewClient(conn).Signers()
		if err != nil || len(ss) == 0 {
			conn.Close()
			return
		}
		signers = append(signers, ss...)
		tried = append(tried, "agent")
		release = func() { conn.Close() }
	}
	if a.agentFirst {
		addAgent()
		addKeys()
	} else {
		addKeys()
		addAgent()
	}
	tried = append(tried, a.skipped...)

	var ms []ssh.AuthMethod
	if len(signers) > 0 {
		ms = append(ms, ssh.PublicKeys(signers...))
	}
	if a.password != "" {
		pw := a.password
		ms = append(ms, ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}))
		tried = append(tried, "password")
	}
	return ms, tried, release
}

// dialAgent connects to SSH_AUTH_SOCK, or on Windows to the OpenSSH agent's
// named pipe when the variable is unset.
func dialAgent() (io.ReadWriteCloser, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" && runtime.GOOS == "windows" {
		sock = windowsAgentPipe
	}
	if sock == "" {
		return nil, errors.New("ssh: no agent")
	}
	if strings.HasPrefix(sock, `\\.\pipe\`) || strings.HasPrefix(sock, `//./pipe/`) {
		return os.OpenFile(sock, os.O_RDWR, 0)
	}
	return net.DialTimeout("unix", sock, 2*time.Second)
}

// isAuthErr reports whether a handshake failed because every auth method was
// refused; x/crypto/ssh has no typed error for it.
func isAuthErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ssh: unable to authenticate")
}

func authError(h hop, tried []string) error {
	if len(tried) == 0 {
		return fmt.Errorf("%w for %s@%s (no agent, keys or password available)", ErrAuth, h.user, h.host)
	}
	return fmt.Errorf("%w for %s@%s (tried: %s)", ErrAuth, h.user, h.host, strings.Join(tried, ", "))
}
