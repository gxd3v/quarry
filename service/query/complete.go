package query

import (
	"context"
	"log/slog"
	"slices"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/complete"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

// Complete suggests completions at the cursor. It only reads the tab's
// binding, so it never waits for a running operation; object names come from
// the catalog cache and are left out when it cannot be loaded.
func (s *Service) Complete(ctx context.Context, req *connect.Request[quarryv1.CompleteRequest]) (*connect.Response[quarryv1.CompleteResponse], error) {
	t, err := s.tab(req.Msg.TabId)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	connID, scope, lang := t.connID, t.scope, t.lang
	t.mu.Unlock()

	script := req.Msg.Script
	cr := complete.Request{Lang: lang, Text: script, Cursor: service.ByteOffset(script, int(req.Msg.Cursor))}
	if cfg, ok := s.live.Config(connID); ok {
		cr.SearchPath = searchPath(cfg.Kind, scope)
	}
	if s.catalog != nil {
		objs, err := s.catalog.Objects(ctx, connID, scope, false)
		if err != nil {
			slog.Debug("query: complete without catalog", "conn", connID, "err", err)
		} else {
			cr.Objects = objs
		}
	}

	r := complete.Suggest(cr)
	res := &quarryv1.CompleteResponse{
		Start: int32(service.UTF16Offset(script, r.Start)),
		End:   int32(service.UTF16Offset(script, r.End)),
		Items: make([]*quarryv1.CompletionItem, len(r.Items)),
	}
	for i, it := range r.Items {
		res.Items[i] = &quarryv1.CompletionItem{Label: it.Label, Insert: it.Insert, Detail: it.Detail, Kind: it.Kind.String()}
	}
	return connect.NewResponse(res), nil
}

// searchPath lists the schemas whose objects resolve without qualification.
func searchPath(kind driver.Kind, scope driver.Scope) []string {
	switch kind {
	case driver.Postgres:
		if scope.Schema == "" {
			return []string{"public"}
		}
		return slices.Compact([]string{scope.Schema, "public"})
	}
	return nil
}
