package term

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

type sys struct {
	proc windows.Handle
	job  windows.Handle // zero when the process could not be put in a job
	out  *io.PipeReader // fed by pump

	mu       sync.Mutex // guards cpty against use after closing
	cpty     *conpty.ConPty
	closed   bool
	released chan struct{} // the console is closed and the handles are free to go
}

func (t *Term) start(cmd *exec.Cmd, cols, rows int) error {
	c, err := conpty.New(cols, rows, 0)
	if err != nil {
		return err
	}
	// The pump reads from its own handle, so closing the console never
	// leaves it reading from a closed (and possibly reused) handle.
	self := windows.CurrentProcess()
	var out windows.Handle
	if err := windows.DuplicateHandle(self, windows.Handle(c.OutPipeReadFd()), self, &out, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		c.Close()
		return err
	}
	_, h, err := c.Spawn(cmd.Path, cmd.Args, &syscall.ProcAttr{Dir: cmd.Dir, Env: cmd.Environ(), Sys: cmd.SysProcAttr})
	if err != nil {
		c.Close()
		windows.CloseHandle(out)
		return err
	}
	pr, pw := io.Pipe()
	t.sys = sys{proc: windows.Handle(h), job: newJob(windows.Handle(h)), out: pr, cpty: c, released: make(chan struct{})}
	go pump(out, pw)
	go t.wait()
	return nil
}

// newJob puts proc in a job that kills its whole tree when closed (or when
// Quarry dies).
func newJob(proc windows.Handle) windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, proc)
	}
	if err != nil {
		windows.CloseHandle(job)
		return 0
	}
	return job
}

// pump copies the console output to the reader until the console is closed,
// discarding it once the reader is gone so the console can always flush.
func pump(h windows.Handle, w *io.PipeWriter) {
	defer windows.CloseHandle(h)
	buf := make([]byte, 32<<10)
	for {
		var n uint32
		err := windows.ReadFile(h, buf, &n, nil)
		if n > 0 {
			w.Write(buf[:n])
		}
		if err != nil {
			w.Close()
			return
		}
	}
}

// wait records the exit and closes the console: ConPTY keeps the output
// pipe open after the process exits, until the console itself is closed.
func (t *Term) wait() {
	_, err := windows.WaitForSingleObject(t.proc, windows.INFINITE)
	var code uint32
	if err == nil {
		err = windows.GetExitCodeProcess(t.proc, &code)
	}
	t.code, t.err = int(code), err
	close(t.exited)

	t.kill()
	t.mu.Lock()
	t.closed = true
	t.cpty.Close()
	t.mu.Unlock()
	close(t.released)
}

func (t *Term) kill() {
	if t.job != 0 {
		windows.TerminateJobObject(t.job, 1)
	} else {
		windows.TerminateProcess(t.proc, 1)
	}
}

func (t *Term) Read(p []byte) (int, error) {
	n, err := t.out.Read(p)
	if errors.Is(err, io.ErrClosedPipe) {
		err = io.EOF
	}
	return n, err
}

func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, ErrClosed
	}
	return t.cpty.Write(p)
}

func (t *Term) resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrClosed
	}
	return t.cpty.Resize(cols, rows)
}

func (t *Term) close() error {
	t.kill()
	t.out.Close()
	<-t.released
	if t.job != 0 {
		windows.CloseHandle(t.job)
	}
	return windows.CloseHandle(t.proc)
}
