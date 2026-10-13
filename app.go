package main

import (
	"context"

	"github.com/gxd3v/quarry/server"
)

// App is bound to the frontend. It only hands out how to reach the Connect
// API; everything else goes through RPC.
type App struct {
	ctx   context.Context
	srv   *server.Server
	bench bool
}

type ServerInfo struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Bench asks the frontend to run its benchmark and report it.
	Bench bool `json:"bench"`
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) shutdown(context.Context) { a.srv.Close() }

func (a *App) ServerInfo() ServerInfo {
	return ServerInfo{URL: a.srv.URL, Token: a.srv.Token, Bench: a.bench}
}
