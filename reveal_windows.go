package main

import (
	"os/exec"
	"syscall"
)

func reveal(path string) error {
	// explorer wants "/select,<path>" unquoted around the switch, so the
	// command line is built by hand instead of by exec's argument quoting.
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	// explorer exits with 1 even when it succeeds.
	_ = cmd.Run()
	return nil
}
