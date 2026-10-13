package redis

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Optional health interfaces, discovered by the UI with type assertions
// (same contract as the PostgreSQL driver):
//
//	if p, ok := conn.(interface{ Ping(context.Context) error }); ok { ... }
//	if a, ok := sess.(interface{ Alive() bool }); ok { ... }
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

const pingTimeout = 5 * time.Second

// Ping sends PING on the default database's metadata client. Failures read
// "connection lost: <cause>"; go-redis reconnects transparently on the next
// call. A closed Conn returns "connection is closed".
func (c *conn) Ping(ctx context.Context) error {
	if c.closed.Load() {
		return errClosed
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pingTimeout)
		defer cancel()
	}
	cl, err := c.client(c.defDB)
	if err != nil {
		return err
	}
	err = cl.Ping(ctx).Err()
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	msg := connectError(err, c.where()).Error()
	msg = strings.TrimPrefix(msg, "cannot connect to "+c.where()+": ")
	return &friendlyError{msg: "connection lost: " + msg, err: err}
}

// Alive reports whether the session's connection is still usable, without
// a round trip: it turns false after a command hit a transport error or was
// interrupted, and true again once the next Execute has reconnected.
func (s *session) Alive() bool { return !s.dead.Load() }
