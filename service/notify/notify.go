// Package notify implements the notify service: it streams server
// notifications (PostgreSQL LISTEN/NOTIFY) to the client.
package notify

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

type Service struct {
	live service.Live
}

var _ quarryv1connect.NotifyServiceHandler = (*Service)(nil)

func New(live service.Live) *Service { return &Service{live: live} }

// Listen streams notifications until the client closes the stream.
func (s *Service) Listen(ctx context.Context, req *connect.Request[quarryv1.ListenRequest], stream *connect.ServerStream[quarryv1.ListenResponse]) error {
	return s.listen(ctx, req.Msg, stream.Send)
}

func (s *Service) listen(ctx context.Context, m *quarryv1.ListenRequest, send func(*quarryv1.ListenResponse) error) error {
	conn, cfg, err := s.live.Live(m.ConnId)
	if err != nil {
		return service.LiveError(err)
	}
	l, ok := conn.(driver.Listener)
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections have no notifications", cfg.Kind))
	}
	if len(m.Channels) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("no channel to listen to"))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Listen calls ready and fn on this goroutine, so sends never overlap.
	var sendErr error
	try := func(r *quarryv1.ListenResponse) {
		if sendErr == nil {
			if sendErr = send(r); sendErr != nil {
				cancel()
			}
		}
	}
	err = l.Listen(ctx, service.ScopeOf(m.Scope), m.Channels,
		func() { try(&quarryv1.ListenResponse{Event: &quarryv1.ListenResponse_Listening{Listening: true}}) },
		func(n driver.Notification) {
			try(&quarryv1.ListenResponse{Event: &quarryv1.ListenResponse_Notification{Notification: &quarryv1.Notification{
				At: timestamppb.New(n.At), Channel: n.Channel, Payload: n.Payload, Pid: int32(n.PID),
			}}})
		})
	if sendErr != nil {
		return sendErr
	}
	if err != nil {
		if qe, ok := errors.AsType[*driver.QueryError](err); ok {
			return connect.NewError(connect.CodeInvalidArgument, qe)
		}
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return nil
}
