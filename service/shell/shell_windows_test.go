package shell

import (
	"testing"

	"golang.org/x/sys/windows"
)

func waitGone(t *testing.T, pid int) {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	if ev, err := windows.WaitForSingleObject(h, 10_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d still running", pid)
	}
}
