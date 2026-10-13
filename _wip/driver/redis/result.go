package redis

import (
	"context"

	"github.com/gxd3v/quarry/internal/driver"
)

// result is a fully read reply laid out as a table. Redis replies arrive
// whole, so paging only slices rows that are already in memory.
type result struct {
	cols []driver.Column
	rows [][]any
	pos  int
}

var _ driver.Result = (*result)(nil)

func (r *result) Columns() []driver.Column { return r.cols }
func (r *result) Affected() int64          { return -1 }

func (r *result) Next(_ context.Context, n int) ([][]any, error) {
	n = max(n, 1)
	if r.pos >= len(r.rows) {
		return nil, nil
	}
	end := min(r.pos+n, len(r.rows))
	out := r.rows[r.pos:end]
	r.pos = end
	return out, nil
}

func (r *result) Close() error {
	r.rows = nil
	r.pos = 0
	return nil
}
