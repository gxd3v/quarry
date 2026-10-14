package oracle

import (
	"context"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Counter = (*conn)(nil)

// CountRows counts the rows of the data view on the metadata session.
func (c *conn) CountRows(ctx context.Context, target *driver.Node, filter string) (int64, error) {
	q, err := c.DataQuery(target, filter, nil)
	if err != nil {
		return 0, err
	}
	var n int64
	err = c.with(ctx, func(l *link) error { return l.queryRow(ctx, "SELECT COUNT(*) FROM ("+q+")", nil, &n) })
	if err != nil {
		return 0, queryError(err, "")
	}
	return n, nil
}
