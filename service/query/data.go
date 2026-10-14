package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

// DataService implements the table view on top of the query tabs.
type DataService struct{ s *Service }

var _ quarryv1connect.DataServiceHandler = (*DataService)(nil)

// Data returns the DataService that shares s's tabs.
func (s *Service) Data() *DataService { return &DataService{s: s} }

func (d *DataService) OpenData(ctx context.Context, req *connect.Request[quarryv1.OpenDataRequest], stream *connect.ServerStream[quarryv1.RunEvent]) error {
	return d.s.openData(ctx, req.Msg, stream.Send)
}

// openData runs the table's data query in the tab as a single statement whose
// text is also the script that error positions refer to. The query passes
// the same guards as Run, without confirmation: a filter is inserted
// verbatim, so it must not smuggle in writes.
func (s *Service) openData(ctx context.Context, req *quarryv1.OpenDataRequest, send func(*quarryv1.RunEvent) error) error {
	t, err := s.tab(req.TabId)
	if err != nil {
		return err
	}
	ctx, release, err := t.begin(ctx)
	if err != nil {
		return err
	}
	defer release()
	connID, _, lang := t.binding()
	conn, cfg, err := s.live.Live(connID)
	if err != nil {
		return service.LiveError(err)
	}
	dv, ok := conn.(driver.DataViewer)
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections have no data view", cfg.Kind))
	}
	text, err := dv.DataQuery(service.NodeOf(req.Node), req.Filter, req.OrderBy)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	st := driver.Statement{Text: text}
	if err := inspect(lang, cfg, []driver.Statement{st}).allow(false); err != nil {
		return err
	}
	return s.execute(ctx, t, text, []stmt{{Statement: st, exec: text}}, s.limit(req.MaxRows), send)
}

func (d *DataService) RowKey(ctx context.Context, req *connect.Request[quarryv1.RowKeyRequest]) (*connect.Response[quarryv1.RowKeyResponse], error) {
	conn, _, err := d.s.live.Live(req.Msg.ConnId)
	if err != nil {
		return nil, service.LiveError(err)
	}
	res := &quarryv1.RowKeyResponse{}
	ed, ok := conn.(driver.Editable)
	if !ok {
		return connect.NewResponse(res), nil
	}
	cols, err := ed.RowKey(ctx, service.NodeOf(req.Msg.Node))
	if err != nil {
		return nil, driverError(err)
	}
	res.Columns = cols
	return connect.NewResponse(res), nil
}

// keyTTLSetter is implemented by connections whose keys expire (Redis).
type keyTTLSetter interface {
	SetKeyTTL(ctx context.Context, target *driver.Node, ttl time.Duration) error
}

func (d *DataService) SetTTL(ctx context.Context, req *connect.Request[quarryv1.SetTTLRequest]) (*connect.Response[quarryv1.SetTTLResponse], error) {
	m := req.Msg
	conn, cfg, err := d.s.live.Live(m.ConnId)
	if err != nil {
		return nil, service.LiveError(err)
	}
	ts, ok := conn.(keyTTLSetter)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections have no key expiry", cfg.Kind))
	}
	if cfg.ReadOnly {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("connection is read-only"))
	}
	if strings.EqualFold(cfg.Env, "prod") && !m.Confirmed {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("confirmation required: changing a key's expiry on a prod connection"))
	}
	if err := ts.SetKeyTTL(ctx, service.NodeOf(m.Node), time.Duration(m.TtlMs)*time.Millisecond); err != nil {
		return nil, driverError(err)
	}
	return connect.NewResponse(&quarryv1.SetTTLResponse{}), nil
}

func (d *DataService) CountRows(ctx context.Context, req *connect.Request[quarryv1.CountRowsRequest]) (*connect.Response[quarryv1.CountRowsResponse], error) {
	m := req.Msg
	conn, cfg, err := d.s.live.Live(m.ConnId)
	if err != nil {
		return nil, service.LiveError(err)
	}
	c, ok := conn.(driver.Counter)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections cannot count rows", cfg.Kind))
	}
	n, err := c.CountRows(ctx, service.NodeOf(m.Node), m.Filter)
	if err != nil {
		return nil, driverError(err)
	}
	return connect.NewResponse(&quarryv1.CountRowsResponse{Count: n}), nil
}

func (d *DataService) ApplyChanges(ctx context.Context, req *connect.Request[quarryv1.ApplyChangesRequest]) (*connect.Response[quarryv1.ApplyChangesResponse], error) {
	m := req.Msg
	conn, cfg, err := d.s.live.Live(m.ConnId)
	if err != nil {
		return nil, service.LiveError(err)
	}
	ed, ok := conn.(driver.Editable)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections cannot edit rows", cfg.Kind))
	}
	if cfg.ReadOnly {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("connection is read-only"))
	}
	if strings.EqualFold(cfg.Env, "prod") && !m.Confirmed {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("confirmation required: saving %d changes to a prod connection", len(m.Changes)))
	}
	changes := make([]driver.RowChange, len(m.Changes))
	for i, c := range m.Changes {
		ch := driver.RowChange{Key: valuesFrom(c.Key), Values: valuesFrom(c.Values)}
		switch c.Op {
		case quarryv1.ChangeOp_CHANGE_OP_INSERT:
			ch.Op = driver.OpInsert
		case quarryv1.ChangeOp_CHANGE_OP_UPDATE:
			ch.Op = driver.OpUpdate
		case quarryv1.ChangeOp_CHANGE_OP_DELETE:
			ch.Op = driver.OpDelete
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("change %d: unknown operation %v", i+1, c.Op))
		}
		changes[i] = ch
	}
	if err := ed.Apply(ctx, service.NodeOf(m.Node), changes); err != nil {
		return nil, driverError(err)
	}
	return connect.NewResponse(&quarryv1.ApplyChangesResponse{}), nil
}

func valuesFrom(m map[string]*quarryv1.Value) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = service.ValueFrom(v)
	}
	return out
}

// driverError maps server errors to InvalidArgument and anything else
// (connection failures, closed connections) to Unavailable.
func driverError(err error) error {
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		msg := qe.Msg
		if qe.Detail != "" {
			msg += ": " + qe.Detail
		}
		return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	}
	// Drivers report conflicts and bad input (stale rows, unknown columns) as
	// plain errors meant for the user.
	return connect.NewError(connect.CodeFailedPrecondition, err)
}
