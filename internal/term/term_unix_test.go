//go:build unix

package term

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func termSize() (cols, rows int) {
	rows, cols, _ = pty.Getsize(os.Stdin)
	return cols, rows
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d still running", pid)
}
