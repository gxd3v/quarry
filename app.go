package main

import "context"

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
