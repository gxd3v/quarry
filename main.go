package main

import (
	"embed"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/applog"
	"github.com/gxd3v/quarry/internal/store"
	"github.com/gxd3v/quarry/server"
	"github.com/gxd3v/quarry/service"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	debug := flag.Bool("debug", false, "verbose logging, also to stderr")
	benchOut := flag.String("bench-out", "", "run the frontend benchmark, write its JSON report to this file and quit")
	flag.Parse()
	closeLog := setupLog(*debug)
	defer closeLog()

	app := &App{bench: *benchOut != ""}
	dev := service.Dev{}
	if app.bench {
		dev.OnReport = func(report string) {
			if err := os.WriteFile(*benchOut, []byte(report), 0o644); err != nil {
				slog.Error("write bench report", "err", err)
			}
			go runtime.Quit(app.ctx)
		}
	}

	mux := http.NewServeMux()
	opts := server.HandlerOptions()
	mux.Handle(quarryv1connect.NewDevServiceHandler(dev, opts...))
	srv, err := server.Start(mux)
	if err != nil {
		slog.Error("start api server", "err", err)
		os.Exit(1)
	}
	app.srv = srv

	err = wails.Run(&options.App{
		Title:            "Quarry",
		Width:            1400,
		Height:           900,
		MinWidth:         800,
		MinHeight:        500,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
		Windows:          &windows.Options{Theme: windows.Dark},
	})
	if err != nil {
		slog.Error("run", "err", err)
		os.Exit(1)
	}
}

// setupLog sends slog to StateDir/quarry.log (and stderr with debug on).
func setupLog(debug bool) (closeLog func()) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	var out io.Writer = os.Stderr
	closeLog = func() {}
	if dir, err := store.StateDir(); err == nil {
		if f, err := applog.Open(filepath.Join(dir, "quarry.log"), 5<<20); err == nil {
			out, closeLog = f, func() { f.Close() }
			if debug {
				out = io.MultiWriter(f, os.Stderr)
			}
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})))
	return closeLog
}
