package redis

import (
	"context"
	"errors"
	"slices"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

// result is a reply laid out as a table. Most replies arrive whole, so
// paging only slices rows already in memory; SCAN-family replies keep
// following their cursor as rows are pulled (see scanner).
type result struct {
	cols []driver.Column
	rows [][]any
	pos  int
	// more fetches the next page of a cursor reply; nil once complete.
	more func(ctx context.Context) (rows [][]any, done bool, err error)
}

var _ driver.Result = (*result)(nil)

func (r *result) Columns() []driver.Column { return r.cols }
func (r *result) Affected() int64          { return -1 }

func (r *result) Next(ctx context.Context, n int) ([][]any, error) {
	n = max(n, 1)
	for len(r.rows)-r.pos < n && r.more != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, done, err := r.more(ctx)
		if done {
			r.more = nil
		}
		if err != nil {
			return nil, err
		}
		r.rows = append(slices.Clone(r.rows[r.pos:]), rows...)
		r.pos = 0
	}
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
	r.more = nil
	return nil
}

// scanner continues a SCAN, HSCAN, SSCAN or ZSCAN from its cursor on the
// session, dropping rows already returned (a scan may repeat elements). On
// a Cluster, a keyless SCAN walks nodes one primary after the other.
type scanner struct {
	s      *session
	args   []string
	ci     int // index of the cursor argument
	cursor string
	nodes  []*goredis.Client
	seen   map[string]bool
}

func (s *session) scanner(args []string, cursor string, nodes []*goredis.Client) *scanner {
	sc := &scanner{s: s, args: slices.Clone(args), ci: 2, cursor: cursor, nodes: nodes, seen: map[string]bool{}}
	if verbOf(args) == "SCAN" {
		sc.ci = 1
	}
	return sc
}

var errScanReply = errors.New("unexpected reply while following the scan cursor")

func (sc *scanner) page(ctx context.Context) ([][]any, bool, error) {
	for {
		if sc.ci >= len(sc.args) {
			return nil, true, errScanReply
		}
		args := slices.Clone(sc.args)
		args[sc.ci] = sc.cursor
		var (
			v   any
			err error
		)
		if len(sc.nodes) > 0 {
			v, err = sc.s.doOn(ctx, sc.nodes[0], args)
		} else {
			v, err = sc.s.do(ctx, args)
		}
		if err != nil {
			return nil, true, sc.s.fail(ctx, err)
		}
		t := tabulate(args, v)
		if t.cursor == "" {
			return nil, true, errScanReply
		}
		sc.cursor = t.cursor
		rows := sc.dedup(t.rows)
		if sc.cursor != "0" {
			return rows, false, nil
		}
		if len(sc.nodes) > 1 {
			sc.nodes = sc.nodes[1:]
			if len(rows) == 0 {
				continue
			}
			return rows, false, nil
		}
		return rows, true, nil
	}
}

func (sc *scanner) dedup(rows [][]any) [][]any {
	out := rows[:0:0]
	for _, r := range rows {
		k := text(r[0])
		if b, ok := r[0].([]byte); ok {
			k = string(b)
		}
		if sc.seen[k] {
			continue
		}
		sc.seen[k] = true
		out = append(out, r)
	}
	return out
}
