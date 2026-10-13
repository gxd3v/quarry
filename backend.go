package main

import (
	"net/http"
	"path/filepath"

	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/postgres"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/internal/workspace"
	"github.com/gxd3v/quarry/server"
	"github.com/gxd3v/quarry/service"
	"github.com/gxd3v/quarry/service/catalog"
	"github.com/gxd3v/quarry/service/connection"
	"github.com/gxd3v/quarry/service/explorer"
	"github.com/gxd3v/quarry/service/query"
	wsvc "github.com/gxd3v/quarry/service/workspace"
)

// backend owns the services and the API server that exposes them.
type backend struct {
	srv   *server.Server
	conns *connection.Manager
	query *query.Service
}

func startBackend(dev service.Dev) (*backend, error) {
	driver.Register(postgres.New())

	connFile, err := store.ConnectionsFile()
	if err != nil {
		return nil, err
	}
	configDir, err := store.ConfigDir()
	if err != nil {
		return nil, err
	}
	stateDir, err := store.StateDir()
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Open(stateDir)
	if err != nil {
		return nil, err
	}
	conns, err := connection.New(connFile, store.Keyring())
	if err != nil {
		return nil, err
	}
	cat := catalog.New(conns)
	b := &backend{conns: conns, query: query.New(conns, query.WithHistory(ws), query.WithCatalog(cat))}

	mux := http.NewServeMux()
	opts := server.HandlerOptions()
	mux.Handle(quarryv1connect.NewDevServiceHandler(dev, opts...))
	mux.Handle(quarryv1connect.NewConnectionServiceHandler(connection.NewService(conns), opts...))
	mux.Handle(quarryv1connect.NewExplorerServiceHandler(explorer.New(conns, cat), opts...))
	mux.Handle(quarryv1connect.NewQueryServiceHandler(b.query, opts...))
	mux.Handle(quarryv1connect.NewDataServiceHandler(b.query.Data(), opts...))
	mux.Handle(quarryv1connect.NewWorkspaceServiceHandler(wsvc.New(ws, filepath.Join(configDir, "settings.json")), opts...))
	if b.srv, err = server.Start(mux); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *backend) Close() {
	if b.srv != nil {
		b.srv.Close()
	}
	b.query.Close()
	b.conns.Close()
}
