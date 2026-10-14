//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func reveal(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start()
	}
	return exec.Command("xdg-open", filepath.Dir(path)).Start()
}
