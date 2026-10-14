//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/gxd3v/quarry/internal/update"
)

// restart starts the updated copy, which waits for this process to exit.
func restart(in update.Install, _ string) error {
	pid := strconv.Itoa(os.Getpid())
	var cmd *exec.Cmd
	if in.Kind == update.AppBundle {
		cmd = exec.Command("open", "-n", in.Target, "--args", "-wait-pid", pid)
	} else {
		cmd = exec.Command(in.Target, "-wait-pid", pid)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// waitExit waits up to d for process pid to end.
func waitExit(pid int, d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
	}
}
