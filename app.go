package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is bound to the frontend. It only hands out how to reach the Connect
// API; everything else goes through RPC.
type App struct {
	ctx     context.Context
	backend *backend
	bench   bool
	closing atomic.Bool
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

// RevealPath shows a file in the system file manager.
func (a *App) RevealPath(path string) error { return reveal(path) }
