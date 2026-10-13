package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"connectrpc.com/connect"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/workspace"
	"github.com/gxd3v/quarry/service"
)

// fakeLive serves fake connections by id.
type fakeLive struct {
	mu    sync.Mutex
	cfgs  map[string]driver.Config
	conns map[string]driver.Conn
}

func newFakeLive() *fakeLive {
	return &fakeLive{cfgs: map[string]driver.Config{}, conns: map[string]driver.Conn{}}
}

func (l *fakeLive) add(id string, kind driver.Kind, c driver.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfgs[id] = driver.Config{ID: uuid.New(), Name: id, Kind: kind}
	if c != nil {
		l.conns[id] = c
	} else {
		delete(l.conns, id)
	}
}

func (l *fakeLive) Live(id string) (driver.Conn, driver.Config, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, ok := l.cfgs[id]
	if !ok {
		return nil, cfg, connect.NewError(connect.CodeNotFound, errors.New("unknown connection"))
	}
	c, ok := l.conns[id]
	if !ok {
		return nil, cfg, service.ErrNotConnected
	}
	return c, cfg, nil
}

func (l *fakeLive) Config(id string) (driver.Config, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, ok := l.cfgs[id]
	return cfg, ok
}

// fakeStmt scripts how a statement behaves, keyed by its text.
type fakeStmt struct {
	cols     int   // > 0: returns rows
	rows     int   // rows returned
	affected int64 // for commands
	err      error // returned by Execute
	rowErr   error // returned by Next once rows run out
	msgs     []driver.Message
	sleep    time.Duration // Execute takes this long
	blockAt  int           // > 0: Next blocks until canceled after this many rows
	blockRun bool          // Execute blocks until canceled
}

type fakeConn struct {
	mu       sync.Mutex
	stmts    map[string]fakeStmt
	noTx     bool
	sessions []*fakeSession
	openErr  error
	// unblocked is signalled each time a blocked call returns on cancel.
	unblocked chan struct{}
}

func newFakeConn(stmts map[string]fakeStmt) *fakeConn {
	return &fakeConn{stmts: stmts, unblocked: make(chan struct{}, 16)}
}

func (c *fakeConn) Children(context.Context, *driver.Node) ([]*driver.Node, error) { return nil, nil }
func (c *fakeConn) Close() error                                                   { return nil }

func (c *fakeConn) NewSession(_ context.Context, scope driver.Scope) (driver.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openErr != nil {
		return nil, c.openErr
	}
	s := &fakeSession{conn: c, scope: scope, autoCommit: true}
	c.sessions = append(c.sessions, s)
	if c.noTx {
		return plainSession{s}, nil
	}
	return s, nil
}

func (c *fakeConn) session(i int) *fakeSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[i]
}

func (c *fakeConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sessions)
}

type fakeSession struct {
	conn       *fakeConn
	scope      driver.Scope
	closed     atomic.Bool
	dead       atomic.Bool
	autoCommit bool
	inTx       atomic.Bool
	cur        *fakeResult

	mu       sync.Mutex
	msgs     []driver.Message
	executed []string
}

// plainSession hides the optional interfaces of fakeSession.
type plainSession struct{ s *fakeSession }

func (p plainSession) Split(script string) []driver.Statement { return nil }
func (p plainSession) Execute(ctx context.Context, st driver.Statement) (driver.Result, error) {
	return p.s.Execute(ctx, st)
}
func (p plainSession) Close() error { return p.s.Close() }

func (s *fakeSession) Split(string) []driver.Statement { return nil }
func (s *fakeSession) Alive() bool                     { return !s.dead.Load() }
func (s *fakeSession) Close() error {
	if s.cur != nil {
		s.cur.Close()
	}
	s.closed.Store(true)
	return nil
}

func (s *fakeSession) DrainMessages() []driver.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgs
	s.msgs = nil
	return m
}

func (s *fakeSession) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.executed...)
}

func (s *fakeSession) Execute(ctx context.Context, st driver.Statement) (driver.Result, error) {
	if s.closed.Load() {
		return nil, errors.New("session is closed")
	}
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
	s.mu.Lock()
	s.executed = append(s.executed, st.Text)
	s.mu.Unlock()
	text := strings.ToLower(st.Text)
	switch text {
	case "begin":
		s.inTx.Store(true)
	case "commit", "rollback":
		s.inTx.Store(false)
	}
	if !s.autoCommit && strings.HasPrefix(text, "insert") {
		s.inTx.Store(true)
	}
	fs, ok := s.conn.stmts[st.Text]
	if !ok {
		fs = fakeStmt{affected: -1}
	}
	s.mu.Lock()
	s.msgs = append(s.msgs, fs.msgs...)
	s.mu.Unlock()
	if fs.sleep > 0 {
		time.Sleep(fs.sleep)
	}
	if fs.blockRun {
		<-ctx.Done()
		s.conn.unblocked <- struct{}{}
		return nil, &driver.QueryError{Msg: "canceling statement due to user request", Code: "57014", Position: -1}
	}
	if fs.err != nil {
		return nil, fs.err
	}
	r := &fakeResult{st: fs, affected: fs.affected, conn: s.conn}
	for i := range fs.cols {
		r.cols = append(r.cols, driver.Column{Name: fmt.Sprintf("c%d", i), Type: "int8"})
	}
	if fs.cols > 0 {
		r.affected = -1
		s.cur = r
	}
	return r, nil
}

func (s *fakeSession) SetAutoCommit(_ context.Context, on bool) error {
	if on {
		s.inTx.Store(false)
	}
	s.autoCommit = on
	return nil
}
func (s *fakeSession) Commit(context.Context) error   { s.inTx.Store(false); return nil }
func (s *fakeSession) Rollback(context.Context) error { s.inTx.Store(false); return nil }
func (s *fakeSession) InTx() bool                     { return s.inTx.Load() }

type fakeResult struct {
	st       fakeStmt
	conn     *fakeConn
	cols     []driver.Column
	pos      int
	affected int64
	closed   atomic.Bool
	sizes    []int
}

func (r *fakeResult) Columns() []driver.Column { return r.cols }
func (r *fakeResult) Affected() int64          { return r.affected }
func (r *fakeResult) Tag() string {
	if len(r.cols) > 0 {
		return fmt.Sprintf("SELECT %d", r.pos)
	}
	return "OK"
}
func (r *fakeResult) Close() error { r.closed.Store(true); return nil }

func (r *fakeResult) Next(ctx context.Context, n int) ([][]any, error) {
	if r.closed.Load() {
		return nil, nil
	}
	r.sizes = append(r.sizes, n)
	if r.st.blockAt > 0 && r.pos >= r.st.blockAt {
		<-ctx.Done()
		r.conn.unblocked <- struct{}{}
		return nil, ctx.Err()
	}
	end := min(r.pos+n, r.st.rows)
	if r.st.blockAt > 0 {
		end = min(end, r.st.blockAt)
	}
	var out [][]any
	for ; r.pos < end; r.pos++ {
		row := make([]any, len(r.cols))
		for i := range row {
			row[i] = int64(r.pos)
		}
		out = append(out, row)
	}
	if len(out) == 0 && r.st.rowErr != nil {
		return nil, r.st.rowErr
	}
	return out, nil
}

// memHistory collects history entries.
type memHistory struct {
	mu      sync.Mutex
	entries []workspace.Entry
	err     error
}

func (h *memHistory) AppendHistory(e workspace.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, e)
	return h.err
}

func (h *memHistory) all() []workspace.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]workspace.Entry(nil), h.entries...)
}
