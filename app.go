package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is bound to the frontend. It only hands out how to reach the Connect
// API; everything else goes through RPC.
type App struct {
	ctx     context.Context
	backend *backend
	bench   bool
}

type ServerInfo struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Bench asks the frontend to run its benchmark and report it.
	Bench bool `json:"bench"`
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) shutdown(context.Context) { a.backend.Close() }

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
