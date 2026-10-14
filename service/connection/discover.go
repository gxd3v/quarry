package connection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/importers"
)

// DiscoverConnections reads the usual connection files of libpq and Oracle
// clients, plus the requested ones, and returns the entries not saved yet.
func (s *Service) DiscoverConnections(_ context.Context, req *connect.Request[quarryv1.DiscoverConnectionsRequest]) (*connect.Response[quarryv1.DiscoverConnectionsResponse], error) {
	var saved []driver.Config
	for _, e := range s.m.list() {
		saved = append(saved, e.cfg)
	}
	found, errs := discover(discoverPaths(req.Msg.Paths))
	res := &quarryv1.DiscoverConnectionsResponse{Errors: errs}
	names := map[string]bool{}
	for _, c := range saved {
		names[strings.ToLower(c.Name)] = true
	}
	for _, d := range found {
		if slices.ContainsFunc(saved, func(c driver.Config) bool { return sameServer(c, d.Config) }) {
			continue
		}
		saved = append(saved, d.Config)
		d.Config.Name = uniqueName(names, d.Name)
		d.Config.Env = "dev"
		if isLocal(d.Config.Host) {
			d.Config.Env = "local"
		}
		dc := &quarryv1.DiscoveredConnection{Config: configProto(d.Config), Source: d.source}
		if d.HasPassword {
			dc.Password = &d.Password
		}
		res.Connections = append(res.Connections, dc)
	}
	return connect.NewResponse(res), nil
}

type discovered struct {
	importers.Entry
	source string
}

// discoverPaths lists the standard files first, then extra, without repeats.
func discoverPaths(extra []string) []string {
	var paths []string
	if p := importers.PgpassPath(); p != "" {
		paths = append(paths, p)
	}
	paths = append(paths, importers.PgServicePaths()...)
	paths = append(paths, importers.TNSNamesPaths()...)
	for _, p := range extra {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// discover parses the files. Named entries (services, TNS aliases) come
// before password file lines, so a line duplicating a service is the one
// dropped; services without a password take it from the password files, as
// libpq would. Missing files are skipped; others that fail are reported as
// "path: error".
func discover(paths []string) ([]discovered, []string) {
	var (
		out, unnamed []discovered
		errs         []string
		lines        []importers.PgpassLine
	)
	fail := func(p string, err error) { errs = append(errs, fmt.Sprintf("%s: %v", p, err)) }
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			fail(p, err)
			continue
		}
		var entries []importers.Entry
		switch importers.Detect(p, data) {
		case importers.Pgpass:
			ls, err := importers.ParsePgpass(bytes.NewReader(data))
			if err != nil {
				fail(p, err)
				continue
			}
			lines = append(lines, ls...)
			for _, e := range importers.PgpassConnections(ls) {
				unnamed = append(unnamed, discovered{e, p})
			}
		case importers.PgService:
			entries, err = importers.ParsePgService(bytes.NewReader(data))
		case importers.TNSNames:
			entries, err = importers.ParseTNSNames(bytes.NewReader(data))
		default:
			err = errors.New("not a .pgpass, pg_service.conf or tnsnames.ora file")
		}
		if err != nil {
			fail(p, err)
			continue
		}
		for _, e := range entries {
			out = append(out, discovered{e, p})
		}
	}
	out = append(out, unnamed...)
	for i := range out {
		e := &out[i].Entry
		if e.Config.Kind == driver.Postgres && !e.HasPassword {
			e.Password, e.HasPassword = importers.PgpassPassword(lines, e.Config)
		}
		if e.Name == "" {
			e.Name = e.Config.Host
			if e.Config.Database != "" {
				e.Name += "/" + e.Config.Database
			}
		}
	}
	return out, errs
}

func sameServer(a, b driver.Config) bool {
	return a.Kind == b.Kind && strings.EqualFold(a.Host, b.Host) && a.Port == b.Port &&
		a.Database == b.Database && a.User == b.User && a.Options["sid"] == b.Options["sid"]
}

// uniqueName returns name, or name with a " (n)" suffix when taken, and
// marks it as taken.
func uniqueName(taken map[string]bool, name string) string {
	n := name
	for i := 2; taken[strings.ToLower(n)]; i++ {
		n = fmt.Sprintf("%s (%d)", name, i)
	}
	taken[strings.ToLower(n)] = true
	return n
}

func isLocal(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasPrefix(host, "/") || filepath.IsAbs(host) // a Unix socket directory
}
