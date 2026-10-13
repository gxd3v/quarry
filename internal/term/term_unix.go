//go:build unix

package term

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

type sys struct {
	f   *os.File // pty master
	pid int      // also the process group id (the child leads its own session)
}

func (t *Term) start(cmd *exec.Cmd, cols, rows int) error {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	t.f, t.pid = f, cmd.Process.Pid
	go func() {
		err := cmd.Wait()
		if ps := cmd.ProcessState; ps != nil {
			t.code = ps.ExitCode()
			if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && t.code >= 0 {
				err = nil
			}
		} else {
			t.code = -1
		}
		t.err = err
		close(t.exited)
	}()
	return nil
}

func (t *Term) Read(p []byte) (int, error) {
	n, err := t.f.Read(p)
	// Linux reports EIO once every slave descriptor is closed.
	if err != nil && (errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)) {
		err = io.EOF
	}
	return n, err
}

func (t *Term) Write(p []byte) (int, error) {
	n, err := t.f.Write(p)
	if errors.Is(err, os.ErrClosed) {
		err = ErrClosed
	}
	return n, err
}

func (t *Term) resize(cols, rows int) error {
	err := pty.Setsize(t.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if errors.Is(err, os.ErrClosed) {
		err = ErrClosed
	}
	return err
}

func (t *Term) close() error {
	// The whole group: the CLI and its children (pager, \! shell, ...).
	_ = syscall.Kill(-t.pid, syscall.SIGKILL)
	err := t.f.Close()
	<-t.exited
	return err
}
