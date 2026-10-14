package connection

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/store"
)

// Service serves the ConnectionService on top of a Manager.
type Service struct {
	m *Manager
}

var _ quarryv1connect.ConnectionServiceHandler = (*Service)(nil)

func NewService(m *Manager) *Service { return &Service{m: m} }

var kinds = []driver.Kind{driver.Postgres, driver.Oracle, driver.MongoDB, driver.Redis}

func (s *Service) ListDrivers(context.Context, *connect.Request[quarryv1.ListDriversRequest]) (*connect.Response[quarryv1.ListDriversResponse], error) {
	res := &quarryv1.ListDriversResponse{}
	for _, k := range kinds {
		if d, ok := s.m.lookup(k); ok {
			res.Drivers = append(res.Drivers, &quarryv1.DriverInfo{
				Kind:        string(k),
				Language:    string(d.Language()),
				DefaultPort: int32(store.DefaultPort(k)),
			})
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ListConnections(context.Context, *connect.Request[quarryv1.ListConnectionsRequest]) (*connect.Response[quarryv1.ListConnectionsResponse], error) {
	entries := s.m.list()
	slices.SortStableFunc(entries, func(a, b entry) int {
		if a.cfg.Favorite != b.cfg.Favorite {
			if a.cfg.Favorite {
				return -1
			}
			return 1
		}
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.cfg.Group), strings.ToLower(b.cfg.Group)),
			cmp.Compare(strings.ToLower(a.cfg.Name), strings.ToLower(b.cfg.Name)),
		)
	})
	res := &quarryv1.ListConnectionsResponse{Connections: make([]*quarryv1.Connection, len(entries))}
	for i, e := range entries {
		res.Connections[i] = &quarryv1.Connection{
			Config:      configProto(e.cfg),
			Status:      e.status,
			HasPassword: s.m.hasPassword(e.cfg.ID),
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) SaveConnection(_ context.Context, req *connect.Request[quarryv1.SaveConnectionRequest]) (*connect.Response[quarryv1.SaveConnectionResponse], error) {
	cfg, err := configOf(req.Msg.Config)
	if err != nil {
		return nil, err
	}
	cfg, err = s.m.save(cfg, req.Msg.Password, req.Msg.ForgetPassword)
	if err == nil {
		err = s.m.storeSSHSecrets(cfg.ID, req.Msg.SshPassword, req.Msg.SshPassphrase)
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.SaveConnectionResponse{Connection: &quarryv1.Connection{
		Config:      configProto(cfg),
		Status:      s.m.status(cfg.ID),
		HasPassword: s.m.hasPassword(cfg.ID),
	}}), nil
}

func (s *Service) DeleteConnection(_ context.Context, req *connect.Request[quarryv1.DeleteConnectionRequest]) (*connect.Response[quarryv1.DeleteConnectionResponse], error) {
	id, err := parseID(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if err := s.m.remove(id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.DeleteConnectionResponse{}), nil
}

func (s *Service) TestConnection(ctx context.Context, req *connect.Request[quarryv1.TestConnectionRequest]) (*connect.Response[quarryv1.TestConnectionResponse], error) {
	cfg, err := configOf(req.Msg.Config)
	if err != nil {
		return nil, err
	}
	latency, err := s.m.test(ctx, cfg, req.Msg.Password)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.TestConnectionResponse{LatencyMs: max(latency.Milliseconds(), 1)}), nil
}

func (s *Service) ParseURI(_ context.Context, req *connect.Request[quarryv1.ParseURIRequest]) (*connect.Response[quarryv1.ParseURIResponse], error) {
	cfg, pw, err := store.ParseURI(req.Msg.Uri)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	res := &quarryv1.ParseURIResponse{Config: configProto(cfg)}
	if pw != "" {
		res.Password = &pw
	}
	return connect.NewResponse(res), nil
}

func (s *Service) Connect(ctx context.Context, req *connect.Request[quarryv1.ConnectRequest]) (*connect.Response[quarryv1.ConnectResponse], error) {
	id, err := parseID(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	st, err := s.m.connect(ctx, id, req.Msg.Password)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.ConnectResponse{Status: st}), nil
}

func (s *Service) Disconnect(_ context.Context, req *connect.Request[quarryv1.DisconnectRequest]) (*connect.Response[quarryv1.DisconnectResponse], error) {
	id, err := parseID(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if err := s.m.disconnect(id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.DisconnectResponse{}), nil
}

func (s *Service) WatchStatus(ctx context.Context, _ *connect.Request[quarryv1.WatchStatusRequest], stream *connect.ServerStream[quarryv1.WatchStatusResponse]) error {
	return s.m.watch(ctx, stream.Send)
}

func configProto(c driver.Config) *quarryv1.ConnectionConfig {
	p := &quarryv1.ConnectionConfig{
		Name:        c.Name,
		Kind:        string(c.Kind),
		Env:         c.Env,
		Host:        c.Host,
		Port:        int32(c.Port),
		User:        c.User,
		Database:    c.Database,
		ReadOnly:    c.ReadOnly,
		Group:       c.Group,
		Favorite:    c.Favorite,
		AskPassword: c.AskPassword,
		Options:     maps.Clone(c.Options),
	}
	if c.ID != uuid.Nil() {
		p.Id = c.ID.String()
	}
	return p
}

// configOf converts a request config; an empty id stays nil (a new
// connection).
func configOf(p *quarryv1.ConnectionConfig) (driver.Config, error) {
	if p == nil {
		return driver.Config{}, connect.NewError(connect.CodeInvalidArgument, errors.New("config is required"))
	}
	c := driver.Config{
		Name:        p.Name,
		Kind:        driver.Kind(p.Kind),
		Env:         p.Env,
		Host:        p.Host,
		Port:        int(p.Port),
		User:        p.User,
		Database:    p.Database,
		ReadOnly:    p.ReadOnly,
		Group:       p.Group,
		Favorite:    p.Favorite,
		AskPassword: p.AskPassword,
		Options:     maps.Clone(p.Options),
	}
	if p.Id != "" {
		id, err := parseID(p.Id)
		if err != nil {
			return c, err
		}
		c.ID = id
	}
	return c, nil
}
