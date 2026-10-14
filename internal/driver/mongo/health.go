package mongo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Optional health interfaces, discovered by the UI with type assertions
// (same contract as the PostgreSQL and Redis drivers):
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

// Ping runs the ping command. Failures read "connection lost: <cause>";
// the client's pool reconnects by itself once the server is back. A closed
// Conn returns "connection is closed".
func (c *conn) Ping(ctx context.Context) error {
	if err := c.check(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pingTimeout)
		defer cancel()
	}
	err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "ping", Value: 1}}).Err()
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return &friendlyError{msg: "connection lost: " + innermost(err).Error(), err: err}
}

// Alive reports whether the session is usable, without a round trip. The
// pool reconnects transparently, so Alive only turns false after an
// operation failed on the network (until one succeeds again), or after
// Close. Safe to call from any goroutine.
func (s *session) Alive() bool {
	return !s.closed.Load() && !s.c.closed.Load() && !s.lost.Load()
}
