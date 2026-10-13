// Package explorer serves the ExplorerService: the object tree, catalog and
// object details of a live connection.
package explorer

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

type Service struct {
	live service.Live
}

var _ quarryv1connect.ExplorerServiceHandler = (*Service)(nil)

func New(live service.Live) *Service { return &Service{live: live} }

func (s *Service) Children(ctx context.Context, req *connect.Request[quarryv1.ChildrenRequest]) (*connect.Response[quarryv1.ChildrenResponse], error) {
	conn, _, err := s.conn(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	nodes, err := conn.Children(ctx, service.NodeOf(req.Msg.Parent))
	if err != nil {
		return nil, driverError(ctx, err)
	}
	res := &quarryv1.ChildrenResponse{Nodes: make([]*quarryv1.Node, len(nodes))}
	for i, n := range nodes {
		res.Nodes[i] = service.NodeProto(n)
	}
	return connect.NewResponse(res), nil
}

func (s *Service) Catalog(ctx context.Context, req *connect.Request[quarryv1.CatalogRequest]) (*connect.Response[quarryv1.CatalogResponse], error) {
	conn, cfg, err := s.conn(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	cat, ok := conn.(driver.Catalog)
	if !ok {
		return nil, unsupported(cfg, "an object catalog")
	}
	objs, err := cat.Objects(ctx, service.ScopeOf(req.Msg.Scope))
	if err != nil {
		return nil, driverError(ctx, err)
	}
	res := &quarryv1.CatalogResponse{Objects: make([]*quarryv1.CatalogObject, len(objs))}
	for i, o := range objs {
		res.Objects[i] = &quarryv1.CatalogObject{
			Schema:  o.Schema,
			Name:    o.Name,
			Kind:    string(o.Kind),
			Detail:  o.Detail,
			Columns: service.ColumnsOf(o.Columns),
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) Describe(ctx context.Context, req *connect.Request[quarryv1.DescribeRequest]) (*connect.Response[quarryv1.DescribeResponse], error) {
	if req.Msg.Node == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("node is required"))
	}
	conn, cfg, err := s.conn(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	desc, ok := conn.(driver.Describer)
	if !ok {
		return nil, unsupported(cfg, "object details")
	}
	d, err := desc.Describe(ctx, service.NodeOf(req.Msg.Node))
	if err != nil {
		return nil, driverError(ctx, err)
	}
	res := &quarryv1.DescribeResponse{}
	if d == nil {
		return connect.NewResponse(res), nil
	}
	res.Columns = make([]*quarryv1.ColumnInfo, len(d.Columns))
	for i, c := range d.Columns {
		res.Columns[i] = &quarryv1.ColumnInfo{
			Name:       c.Name,
			Type:       c.Type,
			Default:    c.Default,
			Comment:    c.Comment,
			Nullable:   c.Nullable,
			PrimaryKey: c.PrimaryKey,
		}
	}
	res.Indexes = detailRows(d.Indexes)
	res.Constraints = detailRows(d.Constraints)
	res.Triggers = detailRows(d.Triggers)
	res.Stats = detailRows(d.Stats)
	res.Ddl = d.DDL
	return connect.NewResponse(res), nil
}

func (s *Service) conn(id string) (driver.Conn, driver.Config, error) {
	conn, cfg, err := s.live.Live(id)
	if err != nil {
		return nil, cfg, service.LiveError(err)
	}
	return conn, cfg, nil
}

func detailRows(rows []driver.DetailRow) []*quarryv1.DetailRow {
	out := make([]*quarryv1.DetailRow, len(rows))
	for i, r := range rows {
		out[i] = &quarryv1.DetailRow{Name: r.Name, Definition: r.Definition}
	}
	return out
}

func unsupported(cfg driver.Config, what string) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections do not provide %s", cfg.Kind, what))
}

// driverError maps a driver failure: the caller giving up, or the server
// not answering as expected.
func driverError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	case ctx.Err() != nil:
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewError(connect.CodeUnavailable, err)
}
