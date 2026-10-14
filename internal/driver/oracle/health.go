package oracle

import (
	"context"
	"errors"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

// Optional health interfaces, discovered by the UI with type assertions
// (see the postgres driver).
type (
	Pinger interface {
		Ping(ctx context.Context) error
	}
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

// Ping checks the metadata session with a server round trip. When that
// session turns out to be dead the error reads "connection lost: <cause>";
// the next Ping or explorer call transparently reconnects. A Conn that was
// closed returns its usual "connection is closed" error.
func (c *conn) Ping(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pingTimeout)
		defer cancel()
	}
	err := c.with(ctx, func(l *link) error { return l.oc.Ping(ctx) })
	if err == nil || errors.Is(err, errClosed) {
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	msg := err.Error()
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		msg = qe.Msg
	} else if _, ok := errors.AsType[*friendlyError](err); !ok {
		msg = innermost(err).Error()
	}
	return &friendlyError{msg: "connection lost: " + msg, err: err}
}

// Alive reports whether the session's connection is still usable. It never
// touches the network, so it only knows about failures seen by earlier
// calls (a dead socket, a session killed by the server, a cancel that had
// to close the connection). Safe to call from any goroutine.
func (s *session) Alive() bool { return s.link.alive() }
