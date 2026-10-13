// Package tunnel opens SSH local port forwards so database drivers can reach
// servers behind a bastion. A Tunnel listens on 127.0.0.1 and forwards every
// accepted connection to the target through an SSH client (optionally chained
// through jump hosts).
package tunnel

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config describes how to reach the SSH server that forwards to the database.
type Config struct {
	Host string
	Port int    // default 22
	User string // default: the current OS user

	// KeyFile is a private key path ("~" is expanded). When empty the agent
	// is tried, then ~/.ssh/id_ed25519, id_ecdsa and id_rsa.
	KeyFile    string
	Passphrase string // for an encrypted key
	Password   string // password auth, tried after keys
	UseAgent   bool   // also use the agent when KeyFile is set

	// Jump hosts ("user@host:port") traversed in order before Host, each
	// authenticated with the same methods.
	Jump []string

	KnownHosts string // default ~/.ssh/known_hosts

	// Accept is called for hosts missing from KnownHosts (trust on first
	// use). Returning true appends the key to KnownHosts. A nil Accept
	// rejects unknown hosts. Changed keys are always rejected.
	Accept func(host string, remote net.Addr, key ssh.PublicKey) bool

	KeepAlive time.Duration // default 30s; negative disables
	Timeout   time.Duration // dial and handshake timeout, default 15s
}

// FromOptions builds a Config from a connection's Options. ok is false when
// no SSH host is configured. An unparsable ssh_port makes Open fail.
func FromOptions(opts map[string]string) (cfg Config, ok bool) {
	get := func(k string) string { return strings.TrimSpace(opts[k]) }
	cfg.Host = get("ssh_host")
	if cfg.Host == "" {
		return Config{}, false
	}
	if p := get("ssh_port"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			n = -1
		}
		cfg.Port = n
	}
	cfg.User = get("ssh_user")
	cfg.KeyFile = get("ssh_key")
	cfg.KnownHosts = get("ssh_known_hosts")
	for j := range strings.SplitSeq(get("ssh_jump"), ",") {
		if j = strings.TrimSpace(j); j != "" {
			cfg.Jump = append(cfg.Jump, j)
		}
	}
	cfg.UseAgent, _ = strconv.ParseBool(get("ssh_agent"))
	return cfg, true
}

type hop struct {
	user, host string
	port       int
}

func (h hop) addr() string { return net.JoinHostPort(h.host, strconv.Itoa(h.port)) }

// hops returns the jump chain followed by the SSH server itself.
func (c *Config) hops() ([]hop, error) {
	var hs []hop
	for _, j := range c.Jump {
		h, err := parseHop(j, c.User)
		if err != nil {
			return nil, err
		}
		hs = append(hs, h)
	}
	if c.Port < 0 || c.Port > 65535 {
		return nil, fmt.Errorf("ssh: invalid port for %s", c.Host)
	}
	return append(hs, hop{user: c.User, host: c.Host, port: c.Port}), nil
}

func parseHop(s, defUser string) (hop, error) {
	h := hop{user: defUser, host: s, port: 22}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		h.user, h.host = s[:i], s[i+1:]
	}
	if host, port, err := net.SplitHostPort(h.host); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return hop{}, fmt.Errorf("ssh: invalid jump host %q", s)
		}
		h.host, h.port = host, n
	}
	if h.host == "" || h.user == "" {
		return hop{}, fmt.Errorf("ssh: invalid jump host %q", s)
	}
	return h, nil
}

func (c *Config) withDefaults() Config {
	d := *c
	if d.Port == 0 {
		d.Port = 22
	}
	if d.User == "" {
		d.User = currentUser()
	}
	if d.KnownHosts == "" {
		d.KnownHosts = "~/.ssh/known_hosts"
	}
	d.KnownHosts = expandHome(d.KnownHosts)
	if d.KeyFile != "" {
		d.KeyFile = expandHome(d.KeyFile)
	}
	if d.KeepAlive == 0 {
		d.KeepAlive = 30 * time.Second
	}
	if d.Timeout <= 0 {
		d.Timeout = 15 * time.Second
	}
	return d
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		if i := strings.LastIndexAny(name, `\`); i >= 0 { // DOMAIN\user on Windows
			name = name[i+1:]
		}
		return name
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("USERNAME")
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// tildePath shortens p for messages ("~/.ssh/id_rsa").
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}
