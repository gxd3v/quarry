package export

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

type fakeLive struct {
	conn driver.Conn // nil: saved but not open
	kind driver.Kind
}

func (l *fakeLive) Live(id string) (driver.Conn, driver.Config, error) {
	cfg := driver.Config{Name: id, Kind: l.kind}
	if id != "pg" {
		return nil, cfg, connect.NewError(connect.CodeNotFound, errors.New("unknown connection"))
	}
	if l.conn == nil {
		return nil, cfg, service.ErrNotConnected
	}
	return l.conn, cfg, nil
}

func (l *fakeLive) Config(id string) (driver.Config, bool) {
	return driver.Config{Name: id, Kind: l.kind}, id == "pg"
}

// fakeStmt scripts a statement's result. Rows are (id int64, name string),
// with a NULL name on row 2.
type fakeStmt struct {
	cols    int
	rows    int
	err     error         // returned by Execute
	delay   time.Duration // each non-empty page takes this long
	blockAt int           // > 0: Next blocks until canceled once this many rows are out
}

type fakeConn struct {
	stmts   map[string]fakeStmt
	reached chan struct{} // closed when a result hits blockAt

	mu       sync.Mutex
	scopes   []driver.Scope
	executed []string
	sessions []*fakeSession
	closed   chan struct{} // closed when the first session closes
}

func newConn(stmts map[string]fakeStmt) *fakeConn {
	return &fakeConn{stmts: stmts, reached: make(chan struct{}), closed: make(chan struct{})}
}

func (c *fakeConn) Children(context.Context, *driver.Node) ([]*driver.Node, error) { return nil, nil }
func (c *fakeConn) Close() error                                                   { return nil }

func (c *fakeConn) NewSession(_ context.Context, scope driver.Scope) (driver.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &fakeSession{c: c}
	c.scopes = append(c.scopes, scope)
	c.sessions = append(c.sessions, s)
	return s, nil
}

func (c *fakeConn) DataQuery(target *driver.Node, filter string, orderBy []string) (string, error) {
	if target == nil || len(target.Path) != 4 {
		return "", errors.New("not a table or view")
	}
	q := fmt.Sprintf("SELECT * FROM %s.%s", target.Path[1], target.Path[3])
	if filter != "" {
		q += " WHERE (" + filter + ")"
	}
	if len(orderBy) > 0 {
		q += " ORDER BY " + strings.Join(orderBy, ", ")
	}
	return q, nil
}

// plainConn hides fakeConn's DataQuery.
type plainConn struct{ driver.Conn }

type fakeSession struct {
	c      *fakeConn
	closed bool
	res    []*fakeResult
}

func (s *fakeSession) Split(script string) []driver.Statement { return split(driver.Postgres, script) }

func (s *fakeSession) Execute(_ context.Context, stmt driver.Statement) (driver.Result, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	s.c.executed = append(s.c.executed, stmt.Text)
	st, ok := s.c.stmts[stmt.Text]
	if !ok {
		return nil, &driver.QueryError{Msg: "syntax error at or near " + stmt.Text, Position: 0}
	}
	if st.err != nil {
		return nil, st.err
	}
	r := &fakeResult{c: s.c, st: st}
	s.res = append(s.res, r)
	return r, nil
}

func (s *fakeSession) Close() error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if !s.closed && len(s.c.sessions) > 0 && s.c.sessions[0] == s {
		close(s.c.closed)
	}
	s.closed = true
	return nil
}

type fakeResult struct {
	c      *fakeConn
	st     fakeStmt
	next   int
	closed bool
}

func (r *fakeResult) Columns() []driver.Column {
	cols := []driver.Column{{Name: "id", Type: "int8"}, {Name: "name", Type: "text", Nullable: true}}
	return cols[:min(r.st.cols, len(cols))]
}

func (r *fakeResult) Next(ctx context.Context, n int) ([][]any, error) {
	if r.st.blockAt > 0 && r.next >= r.st.blockAt {
		close(r.c.reached)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	end := min(r.next+n, r.st.rows)
	if r.st.blockAt > 0 {
		end = min(end, r.st.blockAt)
	}
	var out [][]any
	for ; r.next < end; r.next++ {
		i := r.next + 1
		var name any = fmt.Sprintf("v%d", i)
		if i == 2 {
			name = nil
		}
		out = append(out, []any{int64(i), name}[:len(r.Columns())])
	}
	if len(out) > 0 && r.st.delay > 0 {
		time.Sleep(r.st.delay)
	}
	return out, nil
}

func (r *fakeResult) Affected() int64 { return -1 }
func (r *fakeResult) Close() error    { r.closed = true; return nil }
