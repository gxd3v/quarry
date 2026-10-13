package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"connectrpc.com/connect"
	"github.com/charmbracelet/x/ansi"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	cli "github.com/gxd3v/quarry/internal/shell"
	"github.com/gxd3v/quarry/internal/store"
)

const helperEnv = "QUARRY_SHELL_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helper stands in for the CLI.
func helper(mode string) {
	switch mode {
	case "echo":
		fmt.Printf("ready:%d\n", os.Getpid())
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line == "quit" {
				os.Exit(3)
			} else {
				fmt.Printf("got:%s\n", line)
			}
		}
	case "exit":
		fmt.Println("bye")
		os.Exit(7)
	}
}

type fakeLive map[string]driver.Config

func (l fakeLive) Live(id string) (driver.Conn, driver.Config, error) {
	return nil, driver.Config{}, errors.New("not used")
}

func (l fakeLive) Config(id string) (driver.Config, bool) {
	c, ok := l[id]
	return c, ok
}

type fakeSecrets struct {
	m   map[uuid.UUID]string
	err error
}

func (s *fakeSecrets) Get(id uuid.UUID) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if pw, ok := s.m[id]; ok {
		return pw, nil
	}
	return "", store.ErrNoSecret
}
func (s *fakeSecrets) Set(uuid.UUID, string) error { return nil }
func (s *fakeSecrets) Delete(uuid.UUID) error      { return nil }

// launcher runs the helper in the mode named by the connection's "mode"
// option and records passwords and cleanups.
type launcher struct {
	mu        sync.Mutex
	passwords []string
	cleanups  int
}

func (l *launcher) launch(cfg driver.Config, password string, _ driver.Scope) (*cli.Launch, error) {
	l.mu.Lock()
	l.passwords = append(l.passwords, password)
	l.mu.Unlock()
	mode := cfg.Options["mode"]
	if mode == "fail" {
		return nil, errors.New("bad options")
	}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	cleanup := func() {
		l.mu.Lock()
		l.cleanups++
		l.mu.Unlock()
	}
	return &cli.Launch{Cmd: cmd, Cleanup: cleanup, Display: "helper " + mode}, nil
}

func (l *launcher) state() (passwords []string, cleanups int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.passwords...), l.cleanups
}

type env struct {
	s       *Service
	c       quarryv1connect.ShellServiceClient
	l       *launcher
	secrets *fakeSecrets
	ids     map[string]string // mode -> connection id
}

func newEnv(t *testing.T, opts ...Option) *env {
	t.Helper()
	e := &env{l: &launcher{}, secrets: &fakeSecrets{m: map[uuid.UUID]string{}}, ids: map[string]string{}}
	live := fakeLive{}
	for _, mode := range []string{"echo", "exit", "fail", "ask", "missing"} {
		cfg := driver.Config{ID: uuid.New(), Name: mode, Kind: driver.Postgres, Options: map[string]string{"mode": mode}}
		switch mode {
		case "ask":
			cfg.AskPassword = true
			cfg.Options["mode"] = "fail"
		case "missing":
			cfg.Options["shell_path"] = "quarry-missing-cli-x9"
		}
		live[cfg.ID.String()] = cfg
		e.ids[mode] = cfg.ID.String()
	}
	e.s = New(live, e.secrets, append([]Option{WithLauncher(e.l.launch)}, opts...)...)
	t.Cleanup(e.s.Close)

	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewShellServiceHandler(e.s))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.c = quarryv1connect.NewShellServiceClient(srv.Client(), srv.URL)
	return e
}

// shell is the client side of one StartShell stream.
type shell struct {
	mu      sync.Mutex
	started *quarryv1.ShellStarted
	out     bytes.Buffer
	exited  *quarryv1.ShellExited
	err     error
	done    chan struct{}
	cancel  context.CancelFunc
}

func (e *env) start(t *testing.T, req *quarryv1.StartShellRequest) *shell {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := e.c.StartShell(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	sh := &shell{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(sh.done)
		for stream.Receive() {
			sh.mu.Lock()
			switch ev := stream.Msg().Event.(type) {
			case *quarryv1.StartShellResponse_Started:
				sh.started = ev.Started
			case *quarryv1.StartShellResponse_Output:
				sh.out.Write(ev.Output)
			case *quarryv1.StartShellResponse_Exited:
				sh.exited = ev.Exited
			}
			sh.mu.Unlock()
		}
		sh.mu.Lock()
		sh.err = stream.Err()
		sh.mu.Unlock()
	}()
	return sh
}

func (sh *shell) text() string {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return strings.ReplaceAll(ansi.Strip(sh.out.String()), "\r", "")
}

func (sh *shell) waitFor(t *testing.T, re string) []string {
	t.Helper()
	rx := regexp.MustCompile(re)
	var m []string
	eventually(t, func() bool { m = rx.FindStringSubmatch(sh.text()); return m != nil }, "output %q; got %q", re, sh.text())
	return m
}

func (sh *shell) id(t *testing.T) string {
	t.Helper()
	var id string
	eventually(t, func() bool {
		sh.mu.Lock()
		defer sh.mu.Unlock()
		if sh.started != nil {
			id = sh.started.ShellId
		}
		return id != ""
	}, "started event")
	return id
}

// end waits for the stream to end and returns the exit event and error.
func (sh *shell) end(t *testing.T) (*quarryv1.ShellExited, error) {
	t.Helper()
	select {
	case <-sh.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("stream did not end; output %q", sh.text())
	}
	return sh.exited, sh.err
}

func eventually(t *testing.T, cond func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for "+format, args...)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) cleanups() int {
	_, n := e.l.state()
	return n
}

func TestRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["echo"], Cols: 100, Rows: 30})
	id := sh.id(t)
	sh.mu.Lock()
	display := sh.started.Display
	sh.mu.Unlock()
	if display != "helper echo" {
		t.Errorf("display = %q", display)
	}
	sh.waitFor(t, `ready:\d+`)

	if _, err := e.c.ShellInput(ctx, connect.NewRequest(&quarryv1.ShellInputRequest{ShellId: id, Data: []byte("ping\r")})); err != nil {
		t.Fatal(err)
	}
	sh.waitFor(t, "got:ping")
	if _, err := e.c.ShellResize(ctx, connect.NewRequest(&quarryv1.ShellResizeRequest{ShellId: id, Cols: 120, Rows: 40})); err != nil {
		t.Fatal(err)
	}
	_, err := e.c.ShellResize(ctx, connect.NewRequest(&quarryv1.ShellResizeRequest{ShellId: id, Cols: 0, Rows: 40}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("resize to 0 cols: %v", err)
	}

	e.c.ShellInput(ctx, connect.NewRequest(&quarryv1.ShellInputRequest{ShellId: id, Data: []byte("quit\r")}))
	exited, err := sh.end(t)
	if err != nil {
		t.Fatal(err)
	}
	if exited == nil || exited.Code != 3 || exited.Error != "" {
		t.Fatalf("exited = %v, want code 3", exited)
	}
	eventually(t, func() bool { return e.cleanups() == 1 }, "cleanup")
	_, err = e.c.ShellInput(ctx, connect.NewRequest(&quarryv1.ShellInputRequest{ShellId: id, Data: []byte("x")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("input after exit: %v, want NotFound", err)
	}
}

func TestExitCode(t *testing.T) {
	e := newEnv(t)
	sh := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["exit"]})
	exited, err := sh.end(t)
	if err != nil {
		t.Fatal(err)
	}
	if exited == nil || exited.Code != 7 {
		t.Fatalf("exited = %v, want code 7", exited)
	}
	// All output arrives before the exit.
	if !strings.Contains(sh.text(), "bye") {
		t.Errorf("output = %q", sh.text())
	}
	eventually(t, func() bool { return e.cleanups() == 1 }, "cleanup")
}

func TestStop(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["echo"]})
	id := sh.id(t)
	pid, _ := strconv.Atoi(sh.waitFor(t, `ready:(\d+)`)[1])

	for range 2 {
		if _, err := e.c.StopShell(ctx, connect.NewRequest(&quarryv1.StopShellRequest{ShellId: id})); err != nil {
			t.Fatal(err)
		}
	}
	exited, err := sh.end(t)
	if err != nil {
		t.Fatal(err)
	}
	if exited == nil {
		t.Fatal("no exited event after StopShell")
	}
	waitGone(t, pid)
	eventually(t, func() bool { return e.cleanups() == 1 }, "cleanup")
	_, err = e.c.ShellInput(ctx, connect.NewRequest(&quarryv1.ShellInputRequest{ShellId: id, Data: []byte("x")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("input after stop: %v, want NotFound", err)
	}
	if _, err := e.c.StopShell(ctx, connect.NewRequest(&quarryv1.StopShellRequest{ShellId: "nope"})); err != nil {
		t.Errorf("stop unknown shell: %v", err)
	}
}

func TestClientCancel(t *testing.T) {
	e := newEnv(t)
	sh := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["echo"]})
	id := sh.id(t)
	pid, _ := strconv.Atoi(sh.waitFor(t, `ready:(\d+)`)[1])

	sh.cancel()
	sh.end(t)
	waitGone(t, pid)
	eventually(t, func() bool { return e.cleanups() == 1 }, "cleanup")
	_, err := e.c.ShellInput(context.Background(), connect.NewRequest(&quarryv1.ShellInputRequest{ShellId: id, Data: []byte("x")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("input after cancel: %v, want NotFound", err)
	}
}

func TestServiceClose(t *testing.T) {
	e := newEnv(t)
	sh := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["echo"]})
	sh.id(t)
	pid, _ := strconv.Atoi(sh.waitFor(t, `ready:(\d+)`)[1])

	e.s.Close()
	if n := e.cleanups(); n != 1 {
		t.Errorf("cleanups after Close = %d, want 1", n)
	}
	waitGone(t, pid)
	sh.end(t)
	if n := e.cleanups(); n != 1 {
		t.Errorf("cleanups = %d, want exactly 1", n)
	}

	_, err := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["echo"]}).end(t)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("start after Close: %v, want Unavailable", err)
	}
}

func TestStartErrors(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		req  *quarryv1.StartShellRequest
		code connect.Code
	}{
		{"unknown connection", &quarryv1.StartShellRequest{ConnId: uuid.New().String()}, connect.CodeNotFound},
		{"bad size", &quarryv1.StartShellRequest{ConnId: e.ids["echo"], Cols: -1}, connect.CodeInvalidArgument},
		{"launch error", &quarryv1.StartShellRequest{ConnId: e.ids["fail"]}, connect.CodeFailedPrecondition},
		{"ask password", &quarryv1.StartShellRequest{ConnId: e.ids["ask"]}, connect.CodeUnauthenticated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.start(t, c.req).end(t)
			if connect.CodeOf(err) != c.code {
				t.Errorf("err = %v, want %v", err, c.code)
			}
		})
	}
	if pws, _ := e.l.state(); len(pws) != 1 {
		t.Errorf("launcher called %d times, want once (launch error)", len(pws))
	}
}

func TestPasswords(t *testing.T) {
	e := newEnv(t)
	pw := "given"
	e.secrets.m[uuid.MustParse(e.ids["fail"])] = "s3cret"

	run := func(conn string, password *string) {
		t.Helper()
		_, err := e.start(t, &quarryv1.StartShellRequest{ConnId: conn, Password: password}).end(t)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("err = %v, want the launcher's error", err)
		}
	}
	run(e.ids["ask"], &pw)  // ask_password with a password
	run(e.ids["fail"], nil) // keyring
	run(e.ids["fail"], &pw) // given wins over the keyring
	delete(e.secrets.m, uuid.MustParse(e.ids["fail"]))
	run(e.ids["fail"], nil) // nothing saved
	pws, _ := e.l.state()
	if want := []string{"given", "s3cret", "given", ""}; strings.Join(pws, ",") != strings.Join(want, ",") {
		t.Errorf("passwords = %q, want %q", pws, want)
	}

	e.secrets.err = errors.New("keyring locked")
	_, err := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["fail"]}).end(t)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("keyring error: %v, want Unauthenticated", err)
	}
}

func TestMissingCLI(t *testing.T) {
	e := newEnv(t, WithLauncher(launch))
	_, err := e.start(t, &quarryv1.StartShellRequest{ConnId: e.ids["missing"]}).end(t)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "quarry-missing-cli-x9") {
		t.Errorf("err = %v, want FailedPrecondition naming the CLI", err)
	}
}
