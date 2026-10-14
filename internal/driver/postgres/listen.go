package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Listener = (*conn)(nil)

// Listen runs LISTEN on a connection of its own and waits for notifications.
func (c *conn) Listen(ctx context.Context, scope driver.Scope, channels []string, ready func(), fn func(driver.Notification)) error {
	if c.closed.Load() {
		return errClosed
	}
	if len(channels) == 0 {
		return errors.New("no channel to listen to")
	}
	pc, err := c.connect(ctx, scope.Database, nil, nil)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		pc.Close(cctx)
	}()
	for _, ch := range channels {
		if ch == "" {
			return errors.New("empty channel name")
		}
		if _, err := pc.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			return queryError(err, "")
		}
	}
	ready()
	for {
		n, err := pc.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return queryError(err, "")
		}
		fn(driver.Notification{At: time.Now(), Channel: n.Channel, Payload: n.Payload, PID: n.PID})
	}
}
