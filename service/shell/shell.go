// Package shell implements the shell service: it runs a connection's native
// CLI in a pseudo-terminal and relays it to the embedded terminal.
package shell

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	cli "github.com/gxd3v/quarry/internal/shell"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/internal/term"
	"github.com/gxd3v/quarry/service"
)

const (
	readSize = 32 << 10
	// drainTimeout bounds the wait for the last output after the CLI exits,
	// in case processes it left behind keep the terminal open.
	drainTimeout = 2 * time.Second
)

// Launcher builds the CLI process for a connection, as shell.Command does.
type Launcher func(cfg driver.Config, password string, scope driver.Scope) (*cli.Launch, error)

type Option func(*Service)

// WithLauncher replaces the default launcher (shell.Command).
func WithLauncher(l Launcher) Option { return func(s *Service) { s.launch = l } }

type Service struct {
	live    service.Live
	secrets store.Secrets
	launch  Launcher

	mu     sync.Mutex
	shells map[string]*session
	closed bool
}

var _ quarryv1connect.ShellServiceHandler = (*Service)(nil)

func New(live service.Live, secrets store.Secrets, opts ...Option) *Service {
	s := &Service{live: live, secrets: secrets, launch: launch, shells: map[string]*session{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// launch is the default launcher; a missing client is a precondition the
// user fixes by installing it or setting shell_path.
func launch(cfg driver.Config, password string, scope driver.Scope) (*cli.Launch, error) {
	if _, err := cli.Find(cfg.Kind, cfg.Options["shell_path"]); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return cli.Command(cfg, password, scope)
}

// session is a running CLI.
type session struct {
	term    *term.Term
	cleanup func()
	once    sync.Once
}

// close kills the CLI and removes its credential files.
func (sh *session) close() {
	sh.once.Do(func() {
		sh.term.Close()
		sh.cleanup()
	})
}

// StartShell runs the CLI until it exits, the client closes the stream or
// StopShell is called. Events: started, output..., exited.
func (s *Service) StartShell(ctx context.Context, req *connect.Request[quarryv1.StartShellRequest], stream *connect.ServerStream[quarryv1.StartShellResponse]) error {
	m := req.Msg
	if m.Cols < 0 || m.Rows < 0 || m.Cols > term.MaxSize || m.Rows > term.MaxSize {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid terminal size %dx%d", m.Cols, m.Rows))
	}
	cfg, ok := s.live.Config(m.ConnId)
	if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("connection %q not found", m.ConnId))
	}
	pw, err := s.password(cfg, m.Password)
	if err != nil {
		return err
	}
	l, err := s.launch(cfg, pw, service.ScopeOf(m.Scope))
	if err != nil {
		if ce := new(connect.Error); errors.As(err, &ce) {
			return err
		}
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot start the shell: %w", err))
	}
	cleanup := l.Cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	t, err := term.Start(l.Cmd, int(m.Cols), int(m.Rows))
	if err != nil {
		cleanup()
		return connect.NewError(connect.CodeInternal, fmt.Errorf("starting %s: %w", l.Display, err))
	}

	sh := &session{term: t, cleanup: cleanup}
	id := uuid.New().String()
	if !s.add(id, sh) {
		sh.close()
		return connect.NewError(connect.CodeUnavailable, errors.New("shutting down"))
	}
	defer func() {
		s.remove(id)
		sh.close()
	}()

	err = stream.Send(&quarryv1.StartShellResponse{Event: &quarryv1.StartShellResponse_Started{
		Started: &quarryv1.ShellStarted{ShellId: id, Display: l.Display},
	}})
	if err != nil {
		return err
	}
	return relay(ctx, t, stream.Send)
}

// relay streams the output and then the exit. Only the output goroutine
// sends until it is done, so sends never overlap.
func relay(ctx context.Context, t *term.Term, send func(*quarryv1.StartShellResponse) error) error {
	var outErr error
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		outErr = copyOutput(t, send)
	}()
	canceled := func() error {
		t.Close()
		<-outDone
		return connect.NewError(connect.CodeCanceled, context.Cause(ctx))
	}

	select {
	case <-outDone:
	case <-t.Done():
		select {
		case <-outDone:
		case <-time.After(drainTimeout):
			t.Close()
			<-outDone
		case <-ctx.Done():
			return canceled()
		}
	case <-ctx.Done():
		return canceled()
	}
	if outErr != nil {
		return outErr
	}
	select {
	case <-t.Done():
	case <-ctx.Done():
		return canceled()
	}

	code, err := t.Wait()
	exited := &quarryv1.ShellExited{Code: int32(code)}
	if err != nil {
		exited.Error = err.Error()
	}
	return send(&quarryv1.StartShellResponse{Event: &quarryv1.StartShellResponse_Exited{Exited: exited}})
}

// copyOutput sends each read as one message, so bursts coalesce up to
// readSize. It returns nil when the output ends and the error of a failed
// send.
func copyOutput(t *term.Term, send func(*quarryv1.StartShellResponse) error) error {
	buf := make([]byte, readSize)
	for {
		n, err := t.Read(buf)
		if n > 0 {
			// Send marshals the message before returning, so buf can be reused.
			if err := send(&quarryv1.StartShellResponse{Event: &quarryv1.StartShellResponse_Output{Output: buf[:n]}}); err != nil {
				return err
			}
		}
		if err != nil {
			return nil
		}
	}
}

func (s *Service) ShellInput(_ context.Context, req *connect.Request[quarryv1.ShellInputRequest]) (*connect.Response[quarryv1.ShellInputResponse], error) {
	sh, err := s.get(req.Msg.ShellId)
	if err != nil {
		return nil, err
	}
	if _, err := sh.term.Write(req.Msg.Data); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("shell input: %w", err))
	}
	return connect.NewResponse(&quarryv1.ShellInputResponse{}), nil
}

func (s *Service) ShellResize(_ context.Context, req *connect.Request[quarryv1.ShellResizeRequest]) (*connect.Response[quarryv1.ShellResizeResponse], error) {
	m := req.Msg
	if m.Cols <= 0 || m.Rows <= 0 || m.Cols > term.MaxSize || m.Rows > term.MaxSize {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid terminal size %dx%d", m.Cols, m.Rows))
	}
	sh, err := s.get(m.ShellId)
	if err != nil {
		return nil, err
	}
	if err := sh.term.Resize(int(m.Cols), int(m.Rows)); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("shell resize: %w", err))
	}
	return connect.NewResponse(&quarryv1.ShellResizeResponse{}), nil
}

// StopShell kills the CLI; the StartShell stream then reports the exit. An
// unknown or already stopped shell is not an error.
func (s *Service) StopShell(_ context.Context, req *connect.Request[quarryv1.StopShellRequest]) (*connect.Response[quarryv1.StopShellResponse], error) {
	if sh := s.remove(req.Msg.ShellId); sh != nil {
		sh.close()
	}
	return connect.NewResponse(&quarryv1.StopShellResponse{}), nil
}

// Close stops every shell.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	shells := s.shells
	s.shells = map[string]*session{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, sh := range shells {
		wg.Go(sh.close)
	}
	wg.Wait()
}

func (s *Service) add(id string, sh *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.shells[id] = sh
	return true
}

func (s *Service) remove(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.shells[id]
	delete(s.shells, id)
	return sh
}

func (s *Service) get(id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.shells[id]; ok {
		return sh, nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("shell %q not found", id))
}

// password resolves the password the CLI logs in with: the one given, else
// the keyring's unless the connection asks for it every time.
func (s *Service) password(cfg driver.Config, given *string) (string, error) {
	if given != nil {
		return *given, nil
	}
	if cfg.AskPassword {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("password required"))
	}
	pw, err := s.secrets.Get(cfg.ID)
	switch {
	case err == nil:
		return pw, nil
	case errors.Is(err, store.ErrNoSecret):
		return "", nil
	default:
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("password required: %w", err))
	}
}
