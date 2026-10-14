package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/gxd3v/quarry/internal/update"
)

// restart starts the updated copy, which waits for this process to exit.
// An installer runs first, elevated through UAC by "start", and the app
// starts once it finishes.
func restart(in update.Install, installer string) error {
	pid := strconv.Itoa(os.Getpid())
	if in.Kind == update.Installer {
		cmd := exec.Command("cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: windows.CREATE_NO_WINDOW,
			CmdLine:       `cmd.exe /c start "" /wait "` + installer + `" /S & del "` + installer + `" & start "" "` + in.Target + `" -wait-pid ` + pid,
		}
		return cmd.Start()
	}
	return exec.Command(in.Target, "-wait-pid", pid).Start()
}

// waitExit waits up to d for process pid to end.
func waitExit(pid int, d time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	windows.WaitForSingleObject(h, uint32(d.Milliseconds()))
}
