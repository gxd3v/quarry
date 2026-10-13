// Package store persists Quarry's configuration: where files live, the saved
// connections, connection secrets and connection URIs.
package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const appName = "quarry"

// ConfigDir is the per-user configuration directory: $XDG_CONFIG_HOME/quarry
// (or ~/.config/quarry) on Linux, %AppData%\quarry on Windows and
// ~/Library/Application Support/quarry on macOS.
func ConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appName), nil
}

// StateDir is the per-user directory for state that is not configuration
// (history, session restore): $XDG_STATE_HOME/quarry (or ~/.local/state/quarry)
// on Linux, %LocalAppData%\quarry on Windows and
// ~/Library/Application Support/quarry/state on macOS.
func StateDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		dir := os.Getenv("LocalAppData")
		if dir == "" {
			return "", errors.New("%LocalAppData% is not defined")
		}
		return filepath.Join(dir, appName), nil
	case "darwin", "ios":
		dir, err := ConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "state"), nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", appName), nil
}

// ConnectionsFile is the path of the saved connections file.
func ConnectionsFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "connections.toml"), nil
}
