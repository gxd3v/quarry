// Package shell launches a connection's native command-line client (psql,
// SQLcl/SQL*Plus, mongosh, redis-cli) already authenticated and positioned at
// an editor tab's scope.
//
// Passwords never appear in argv. Each client gets them through a channel
// other users' process listings cannot see: a pgpass file (psql), a login
// script (Oracle, MongoDB) or the REDISCLI_AUTH variable (redis-cli). Files
// are written with mode 0600 inside a 0700 directory created per launch under
// os.TempDir and removed by Launch.Cleanup. On Windows that directory lives
// under the user's profile (%TEMP%), whose ACL already restricts it to the
// current user.
//
// Read-only connections: psql gets ON_ERROR_STOP and
// default_transaction_read_only=on. SQLcl/SQL*Plus, mongosh and redis-cli have
// no client-side read-only switch, so nothing is added for them.
package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// Launch is a ready-to-run client process.
type Launch struct {
	Cmd *exec.Cmd
	// Cleanup removes temporary credential files. Call it after the process
	// exits; calling it more than once is harmless.
	Cleanup func()
	// Display describes the command without secrets, for the status bar.
	Display string
}

// clients lists the executables tried for each kind, in order of preference.
var clients = map[driver.Kind][]string{
	driver.Postgres: {"psql"},
	driver.Oracle:   {"sql", "sqlplus"},
	driver.MongoDB:  {"mongosh", "mongo"},
	driver.Redis:    {"redis-cli"},
}

// Find locates the client for kind. A non-empty override (the connection's
// shell_path option) is used instead of the search; it may be a name looked
// up in PATH or a path to the executable.
func Find(kind driver.Kind, override string) (string, error) {
	names, ok := clients[kind]
	if !ok {
		return "", fmt.Errorf("no shell for connection type %q", kind)
	}
	if override != "" {
		p, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("shell_path %q not found or not executable", override)
		}
		return p, nil
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH (set shell_path in the connection options)", strings.Join(names, " or "))
}

// Command builds the client process for cfg, authenticated with password and
// positioned at scope. Stdin, Stdout and Stderr are left nil for the caller
// (tea.ExecProcess) to wire to the terminal.
func Command(cfg driver.Config, password string, scope driver.Scope) (*Launch, error) {
	path, err := Find(cfg.Kind, cfg.Options["shell_path"])
	if err != nil {
		return nil, err
	}
	p := &plan{}
	switch cfg.Kind {
	case driver.Postgres:
		err = postgres(p, cfg, password, scope)
	case driver.Oracle:
		err = oracle(p, cfg, password, scope)
	case driver.MongoDB:
		err = mongo(p, cfg, password, scope)
	case driver.Redis:
		err = redis(p, cfg, password, scope)
	}
	if err != nil {
		p.cleanup()
		return nil, err
	}
	cmd := exec.Command(path, p.args...)
	cmd.Env = mergeEnv(os.Environ(), p.drop, p.env)
	return &Launch{
		Cmd:     cmd,
		Cleanup: p.cleanup,
		Display: strings.Join(append([]string{clientName(path)}, p.display...), " "),
	}, nil
}

// plan collects what a kind-specific builder needs from the process.
type plan struct {
	args    []string
	env     []string // KEY=VALUE additions
	drop    []string // inherited variables to remove
	display []string // secret-free arguments shown to the user
	dir     string   // per-launch temp dir, created on first secret
}

func (p *plan) setenv(k, v string) { p.env = append(p.env, k+"="+v) }

// writeSecret writes content to a 0600 file in the launch's private
// directory and returns its path.
func (p *plan) writeSecret(name, content string) (string, error) {
	if p.dir == "" {
		dir, err := os.MkdirTemp("", "quarry-shell-")
		if err != nil {
			return "", fmt.Errorf("creating temp dir: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("securing temp dir: %w", err)
		}
		p.dir = dir
	}
	path := filepath.Join(p.dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", name, err)
	}
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", name, err)
	}
	return path, nil
}

func (p *plan) cleanup() {
	if p.dir != "" {
		os.RemoveAll(p.dir)
	}
}

// mergeEnv returns base without the drop variables and those set by add,
// followed by add. Keys compare case-insensitively, as on Windows.
func mergeEnv(base, drop, add []string) []string {
	keys := slices.Clone(drop)
	for _, kv := range add {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	out := make([]string, 0, len(base)+len(add))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k != "" && slices.ContainsFunc(keys, func(d string) bool { return strings.EqualFold(d, k) }) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, add...)
}

// clientName is the executable's name without directory or Windows extension.
func clientName(path string) string {
	name := filepath.Base(path)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".bat", ".cmd":
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}
