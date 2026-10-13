package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

// Optional health interfaces. They are not part of the driver contract; the
// UI discovers them with type assertions:
//
//	if p, ok := conn.(interface{ Ping(context.Context) error }); ok { ... }
//	if a, ok := sess.(interface{ Alive() bool }); ok { ... }
type (
	// Pinger is implemented by connections that can check the server is
	// still reachable.
	Pinger interface {
		Ping(ctx context.Context) error
	}
	// AliveChecker is implemented by sessions that can tell, without a round
	// trip, whether their connection has been lost.
	AliveChecker interface {
		Alive() bool
	}
)

var (
	_ Pinger       = (*conn)(nil)
	_ AliveChecker = (*session)(nil)
)

// pingTimeout bounds Ping when ctx has no deadline.
const pingTimeout = 5 * time.Second

// Ping runs an empty query on the default database's metadata connection.
// When that connection turns out to be dead (server restarted, backend
// terminated, network gone) the error reads "connection lost: <cause>"; the
// next Ping or explorer call transparently reconnects. A Conn that was
// closed returns its usual "connection is closed" error.
func (c *conn) Ping(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pingTimeout)
		defer cancel()
	}
	err := c.with(ctx, "", func(pc *pgx.Conn) error { return pc.Ping(ctx) })
	if err == nil || errors.Is(err, errClosed) {
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	msg := err.Error()
	if _, ok := errors.AsType[*driver.QueryError](err); !ok {
		if _, ok := errors.AsType[*friendlyError](err); !ok {
			msg = innermost(err).Error()
		}
	}
	return &friendlyError{msg: "connection lost: " + msg, err: err}
}

// Alive reports whether the session's connection is still usable. It never
// touches the network, so it only knows about failures seen by earlier
// operations (an Execute that hit a dead socket, a server-initiated
// disconnect). Safe to call from any goroutine, even while Execute runs.
func (s *session) Alive() bool {
	select {
	case <-s.pc.PgConn().CleanupDone():
		return false
	default:
		return true
	}
}
