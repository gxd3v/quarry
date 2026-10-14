package query

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
)

// Explain shows the plan of one statement on the tab's session. Server
// errors come back in the response, with positions into the script, as Run
// reports them.
func (s *Service) Explain(ctx context.Context, req *connect.Request[quarryv1.ExplainRequest]) (*connect.Response[quarryv1.ExplainResponse], error) {
	m := req.Msg
	t, err := s.tab(m.TabId)
	if err != nil {
		return nil, err
	}
	ctx, release, err := t.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	connID, _, lang := t.binding()
	cfg, err := s.config(connID)
	if err != nil {
		return nil, err
	}
	stmts := statements(splitterFor(cfg.Kind), m.Script, m.Mode, m.Start, m.End)
	switch len(stmts) {
	case 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no statement to explain"))
	case 1:
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("explain needs a single statement, %d selected", len(stmts)))
	}
	if m.Analyze {
		if err := inspect(lang, cfg, stmts).allow(m.Confirmed); err != nil {
			return nil, err
		}
	}
	plan, err := bind(lang, cfg.Kind, stmts, m.Params)
	if err != nil {
		return nil, err
	}
	st := plan[0]

	sess, _, err := s.session(ctx, t)
	if err != nil {
		return nil, err
	}
	ex, ok := sess.(driver.Explainer)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s sessions cannot explain statements", cfg.Kind))
	}
	t.discard()
	node, err := ex.Explain(ctx, driver.Statement{Text: st.exec, Offset: st.Offset}, m.Analyze)
	if err != nil && ctx.Err() != nil {
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	}

	conv := utf16Conv{s: m.Script}
	res := &quarryv1.ExplainResponse{Range: rangeOf(&conv, st.Statement), Tx: t.txState()}
	if err != nil {
		res.Error = queryError(m.Script, st, err)
	} else {
		res.Plan = planOf(node)
	}
	if ms, ok := sess.(driver.MessageSource); ok {
		for _, msg := range ms.DrainMessages() {
			res.Messages = append(res.Messages, &quarryv1.ServerMessage{Severity: msg.Severity, Text: msg.Text})
		}
	}
	return connect.NewResponse(res), nil
}

func planOf(n *driver.PlanNode) *quarryv1.PlanNode {
	p := &quarryv1.PlanNode{
		Op: n.Op, Target: n.Target, Cost: n.Cost, Rows: n.Rows,
		Actual: n.Actual, ActRows: n.ActRows, Props: n.Props,
	}
	for _, c := range n.Children {
		p.Children = append(p.Children, planOf(c))
	}
	return p
}
