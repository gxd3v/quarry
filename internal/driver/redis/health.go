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
	fe := connectError(err, c.where()).(*friendlyError)
	msg := strings.TrimPrefix(fe.msg, "cannot connect to "+c.where()+": ")
	return &friendlyError{msg: "connection lost: " + msg, err: fe.err}
}

// Alive reports whether the session is usable, without a round trip. A
// connection broken by a cancelled command or a network failure is
// re-established by the next Execute (restoring the selected database), so
// Alive only turns false once such a reconnect failed, or after Close.
// Safe to call from any goroutine.
func (s *session) Alive() bool { return !s.lost.Load() }
