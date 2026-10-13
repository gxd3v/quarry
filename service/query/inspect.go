package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/params"
	"github.com/gxd3v/quarry/internal/sqlsafe"
)

// Inspect reports what a Run with the same arguments would execute. It only
// reads the tab's binding and the saved config, so it works without an open
// session and never waits for a running operation.
func (s *Service) Inspect(_ context.Context, req *connect.Request[quarryv1.InspectRequest]) (*connect.Response[quarryv1.InspectResponse], error) {
	t, err := s.tab(req.Msg.TabId)
	if err != nil {
		return nil, err
	}
	connID, _, lang := t.binding()
	cfg, err := s.config(connID)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	script := m.Script
	stmts := statements(splitterFor(cfg.Kind), script, m.Mode, m.Start, m.End)
	g := inspect(lang, cfg, stmts)

	res := &quarryv1.InspectResponse{
		Statements:        make([]*quarryv1.StatementCheck, len(stmts)),
		Params:            g.params,
		NeedsConfirmation: g.confirm,
		Blocked:           g.blocked,
		Env:               cfg.Env,
	}
	conv := utf16Conv{s: script}
	for i, st := range stmts {
		r := g.reports[i]
		res.Statements[i] = &quarryv1.StatementCheck{Range: rangeOf(&conv, st), Risk: riskName(r.Risk), Verb: r.Verb, Reason: r.Reason}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) config(connID string) (driver.Config, error) {
	cfg, ok := s.live.Config(connID)
	if !ok {
		return cfg, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown connection %q", connID))
	}
	return cfg, nil
}

// guard is the safety verdict on the statements of a run.
type guard struct {
	reports []sqlsafe.Report
	worst   sqlsafe.Report // the riskiest statement, the first on ties
	params  []string       // distinct parameter names in order of first use
	confirm bool           // the run needs confirmed=true
	blocked bool           // read-only connection and some statement writes
}

func inspect(lang driver.Language, cfg driver.Config, stmts []driver.Statement) guard {
	g := guard{reports: make([]sqlsafe.Report, len(stmts))}
	seen := map[string]bool{}
	for i, st := range stmts {
		r := sqlsafe.Analyze(lang, st.Text)
		g.reports[i] = r
		if r.Risk > g.worst.Risk {
			g.worst = r
		}
		if cfg.ReadOnly && sqlsafe.IsWrite(lang, st.Text) {
			g.blocked = true
		}
		for _, p := range params.Find(lang, st.Text) {
			if !seen[p.Name] {
				seen[p.Name] = true
				g.params = append(g.params, p.Name)
			}
		}
	}
	prod := strings.EqualFold(cfg.Env, "prod")
	g.confirm = prod && g.worst.Risk >= sqlsafe.Write || g.worst.Risk >= sqlsafe.UnboundedWrite
	return g
}

// allow enforces the verdict before anything runs.
func (g guard) allow(confirmed bool) error {
	if g.blocked {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("connection is read-only"))
	}
	if g.confirm && !confirmed {
		reason := g.worst.Reason
		if reason == "" {
			reason = g.worst.Verb
		}
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("confirmation required: %s", reason))
	}
	return nil
}

func riskName(r sqlsafe.Risk) string {
	switch r {
	case sqlsafe.Write:
		return "write"
	case sqlsafe.DDL:
		return "ddl"
	case sqlsafe.UnboundedWrite:
		return "unbounded_write"
	case sqlsafe.Destructive:
		return "destructive"
	}
	return "none"
}

// bind substitutes parameter values into the statements that have
// parameters, failing with every missing name before anything runs.
func bind(lang driver.Language, kind driver.Kind, stmts []driver.Statement, values map[string]string) ([]stmt, error) {
	var missing []string
	found := make([][]params.Param, len(stmts))
	for i, st := range stmts {
		found[i] = params.Find(lang, st.Text)
		for _, p := range found[i] {
			label := st.Text[p.Positions[0]:p.Positions[0]+1] + p.Name
			if _, ok := values[p.Name]; !ok && !slices.Contains(missing, label) {
				missing = append(missing, label)
			}
		}
	}
	if len(missing) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no value for parameter %s", strings.Join(missing, ", ")))
	}
	out := make([]stmt, len(stmts))
	for i, st := range stmts {
		out[i] = stmt{Statement: st, exec: st.Text}
		if len(found[i]) == 0 {
			continue
		}
		text, err := params.Substitute(lang, st.Text, values, string(kind))
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		out[i].exec, out[i].bound = text, true
	}
	return out, nil
}
