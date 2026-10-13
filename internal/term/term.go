// Package term runs a process attached to a pseudo-terminal: a pty on Unix
// (creack/pty) and a pseudo console on Windows (ConPTY).
//
// The process runs in its own process group (Unix) or job object (Windows),
// so Close kills it together with whatever it started.
package term

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
)

const (
	DefaultCols = 80
	DefaultRows = 24
	// MaxSize bounds both dimensions (ConPTY stores them as int16).
	MaxSize = 1<<15 - 1
)

// ErrClosed is returned by Write and Resize once the terminal is closed.
var ErrClosed = errors.New("terminal closed")

// Term is a process running in a pseudo-terminal. Read returns its output and
// io.EOF once the process has exited and the output has been drained, or
// after Close. Close must always be called to release the terminal.
type Term struct {
	sys

	exited    chan struct{}
	code      int
	err       error
	closeOnce sync.Once
	closeErr  error
}

// Start runs cmd in a new pseudo-terminal of cols x rows; zero or negative
// sizes take the defaults. Stdin, Stdout and Stderr of cmd must be nil.
func Start(cmd *exec.Cmd, cols, rows int) (*Term, error) {
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	if cols > MaxSize || rows > MaxSize {
		return nil, fmt.Errorf("invalid terminal size %dx%d", cols, rows)
	}
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("term: command must not have its standard streams set")
	}
	t := &Term{exited: make(chan struct{})}
	if err := t.start(cmd, cols, rows); err != nil {
		return nil, err
	}
	return t, nil
}

// Resize changes the terminal size.
func (t *Term) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > MaxSize || rows > MaxSize {
		return fmt.Errorf("invalid terminal size %dx%d", cols, rows)
	}
	return t.resize(cols, rows)
}

// Done is closed when the process exits.
func (t *Term) Done() <-chan struct{} { return t.exited }

// Wait waits for the process to exit and returns its exit code. A non-zero
// exit is not an error; err reports a process killed by a signal or one that
// could not be waited for.
func (t *Term) Wait() (exitCode int, err error) {
	<-t.exited
	return t.code, t.err
}

// Close kills the process and anything it started, if still running, and
// releases the terminal. It is safe to call more than once.
func (t *Term) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.close() })
	return t.closeErr
}
