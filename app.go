package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/internal/update"
	"github.com/gxd3v/quarry/internal/workspace"
)

// App is bound to the frontend. It only hands out how to reach the Connect
// API; everything else goes through RPC.
type App struct {
	ctx     context.Context
	backend *backend
	bench   bool
	closing atomic.Bool
	updates *update.Checker
}

type ServerInfo struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Bench asks the frontend to run its benchmark and report it.
	Bench bool `json:"bench"`
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) shutdown(context.Context) { a.backend.Close() }

// closeGrace bounds how long the window waits for the frontend to save.
const closeGrace = 3 * time.Second

// beforeClose holds the first close request so the frontend can save its
// session; it answers with ConfirmClose, or the window closes after closeGrace.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if a.bench || !a.closing.CompareAndSwap(false, true) {
		return false
	}
	runtime.EventsEmit(ctx, "quarry:before-close")
	time.AfterFunc(closeGrace, func() { runtime.Quit(ctx) })
	return true
}

// ConfirmClose is called by the frontend once its state is saved.
func (a *App) ConfirmClose() {
	a.closing.Store(true)
	runtime.Quit(a.ctx)
}

func (a *App) ServerInfo() ServerInfo {
	srv := a.backend.srv
	return ServerInfo{URL: srv.URL, Token: srv.Token, Bench: a.bench}
}

var scriptFilters = []runtime.FileFilter{
	{DisplayName: "Scripts (*.sql, *.js, *.redis, *.txt)", Pattern: "*.sql;*.js;*.redis;*.txt"},
	{DisplayName: "All files", Pattern: "*"},
}

// OpenScriptDialog asks for a script to open; "" means cancelled.
func (a *App) OpenScriptDialog() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "Open script", Filters: scriptFilters})
}

// SaveScriptDialog asks where to save a script; "" means cancelled.
func (a *App) SaveScriptDialog(name string) (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "Save script", DefaultFilename: name, Filters: scriptFilters})
}

var exportFilters = map[string]runtime.FileFilter{
	"csv":      {DisplayName: "CSV (*.csv)", Pattern: "*.csv"},
	"tsv":      {DisplayName: "TSV (*.tsv)", Pattern: "*.tsv"},
	"json":     {DisplayName: "JSON (*.json)", Pattern: "*.json"},
	"ndjson":   {DisplayName: "NDJSON (*.ndjson, *.jsonl)", Pattern: "*.ndjson;*.jsonl"},
	"sql":      {DisplayName: "SQL (*.sql)", Pattern: "*.sql"},
	"markdown": {DisplayName: "Markdown (*.md)", Pattern: "*.md"},
}

// SaveExportDialog asks where to write an export; "" means cancelled.
func (a *App) SaveExportDialog(name, format string) (string, error) {
	var filters []runtime.FileFilter
	if f, ok := exportFilters[format]; ok {
		filters = append(filters, f)
	}
	filters = append(filters, runtime.FileFilter{DisplayName: "All files", Pattern: "*"})
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "Export to", DefaultFilename: name, Filters: filters})
}

// OpenFileDialog asks for a file to read; pattern is like "*.csv;*.txt" and
// may be empty. "" means cancelled.
func (a *App) OpenFileDialog(title, kind, pattern string) (string, error) {
	var filters []runtime.FileFilter
	if pattern != "" {
		filters = append(filters, runtime.FileFilter{DisplayName: kind, Pattern: pattern})
	}
	filters = append(filters, runtime.FileFilter{DisplayName: "All files", Pattern: "*"})
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: title, Filters: filters, ShowHiddenFiles: true})
}

type UpdateInfo struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	URL     string `json:"url"`
	Notes   string `json:"notes"`
	Date    string `json:"date"`
	Newer   bool   `json:"newer"`
	// How the update installs (see update.Kind); "managed" means it cannot.
	Install string `json:"install"`
}

// CheckForUpdate looks for a newer release; development builds never have
// one. GitHub answers unchanged releases with 304, so frequent checks stay
// within its anonymous rate limit.
func (a *App) CheckForUpdate() (UpdateInfo, error) {
	info := UpdateInfo{Current: version}
	if version == "dev" {
		return info, nil
	}
	r, err := a.updates.Latest(a.ctx)
	if r.Version == "" {
		return info, err
	}
	info.Latest, info.URL, info.Notes, info.Newer = r.Version, r.URL, r.Notes, update.Newer(version, r.Version)
	if !r.Date.IsZero() {
		info.Date = r.Date.Format(time.RFC3339)
	}
	info.Install = string(a.install().Kind)
	return info, nil
}

func (a *App) install() update.Install {
	exe, err := os.Executable()
	if err != nil {
		return update.Install{Kind: update.Managed}
	}
	return update.Detect(exe, goruntime.GOOS, os.Getenv, update.Writable)
}

// InstallUpdate downloads release version, puts it in place of this copy
// and restarts into it. The frontend saves the session first. Progress goes
// out as "quarry:update-progress" events (bytes done, total).
func (a *App) InstallUpdate(target string) error {
	if version == "dev" {
		return errors.New("development builds are not updated")
	}
	r, _ := a.updates.Latest(a.ctx)
	if r.Version != target || !update.Newer(version, r.Version) {
		return fmt.Errorf("%s is not the latest release", target)
	}
	in := a.install()
	if in.Kind == update.Managed {
		return errors.New("this copy is managed by a package manager, which installs its updates")
	}
	var last time.Time
	progress := func(done, total int64) {
		if time.Since(last) > 100*time.Millisecond || done == total {
			last = time.Now()
			runtime.EventsEmit(a.ctx, "quarry:update-progress", done, total)
		}
	}
	installer, err := update.Apply(a.ctx, &http.Client{}, r, in, progress)
	if err != nil {
		return err
	}
	// The new copy reopens what is open now (see TakeReconnect).
	if b, err := json.Marshal(a.backend.conns.OpenIDs()); err == nil {
		if err := workspace.WriteAtomic(reconnectFile(), b, 0o600); err != nil {
			slog.Warn("save connections to reopen", "err", err)
		}
	}
	if err := restart(in, installer); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	a.closing.Store(true)
	runtime.Quit(a.ctx)
	return nil
}

// TakeReconnect returns, once, the connections that were open before an
// update restarted the app.
func (a *App) TakeReconnect() []string {
	p := reconnectFile()
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	os.Remove(p)
	var ids []string
	if json.Unmarshal(b, &ids) != nil {
		return nil
	}
	return ids
}

func reconnectFile() string {
	dir, err := store.StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reconnect.json")
}

// RevealPath shows a file in the system file manager.
func (a *App) RevealPath(path string) error { return reveal(path) }
