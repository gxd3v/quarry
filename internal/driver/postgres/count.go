package postgres

import (
	"context"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Counter = (*conn)(nil)

// CountRows counts the rows of the data view on a connection of its own.
func (c *conn) CountRows(ctx context.Context, target *driver.Node, filter string) (int64, error) {
	if c.closed.Load() {
		return 0, errClosed
	}
	q, err := c.DataQuery(target, filter, nil)
	if err != nil {
		return 0, err
	}
	pc, err := c.connect(ctx, target.Path[0], nil, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		pc.Close(cctx)
	}()
	// The filter is user text: count in a read-only transaction.
	tx, err := pc.Begin(ctx)
	if err != nil {
		return 0, queryError(err, "")
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		tx.Rollback(rctx)
	}()
	if _, err := tx.Exec(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return 0, queryError(err, "")
	}
	var n int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM ("+q+") q").Scan(&n); err != nil {
		return 0, queryError(err, "")
	}
	return n, nil
}
