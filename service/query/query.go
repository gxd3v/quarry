// Package query implements QueryService: editor tabs, each with a dedicated
// driver session, that run scripts and stream their results.
package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/workspace"
	"github.com/gxd3v/quarry/service"
)

// DefaultMaxRows is how many rows a result streams before pausing for Fetch.
const DefaultMaxRows = 10000

// HistorySink records executed statements; *workspace.Store implements it.
type HistorySink interface {
	AppendHistory(workspace.Entry) error
}

type Option func(*Service)

// WithHistory records every executed statement in h.
func WithHistory(h HistorySink) Option { return func(s *Service) { s.history = h } }

// WithMaxRows sets the rows a result streams when a request asks for the default.
func WithMaxRows(n int) Option { return func(s *Service) { s.maxRows = n } }

// WithLanguage replaces the driver registry as the source of tab languages.
func WithLanguage(f func(driver.Kind) (driver.Language, bool)) Option {
	return func(s *Service) { s.language = f }
}

type Service struct {
	live     service.Live
	history  HistorySink
	maxRows  int
	language func(driver.Kind) (driver.Language, bool)

	mu   sync.Mutex
	tabs map[string]*tab
}

var _ quarryv1connect.QueryServiceHandler = (*Service)(nil)

func New(live service.Live, opts ...Option) *Service {
	s := &Service{
		live:     live,
		maxRows:  DefaultMaxRows,
		language: registryLanguage,
		tabs:     map[string]*tab{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func registryLanguage(k driver.Kind) (driver.Language, bool) {
	d, ok := driver.Lookup(k)
	if !ok {
		return "", false
	}
	return d.Language(), true
}

// Close cancels running operations, waits for them and closes every session.
func (s *Service) Close() {
	s.mu.Lock()
	tabs := s.tabs
	s.tabs = map[string]*tab{}
	s.mu.Unlock()
	for _, t := range tabs {
		if done := t.close(); done != nil {
			<-done
		}
	}
}

// tab is one editor tab. Fields below mu are guarded by it; the session and
// its open result are owned by whoever holds the tab busy, or by close once
// the tab is idle.
type tab struct {
	mu     sync.Mutex
	connID string
	scope  driver.Scope
	busy   bool
	closed bool
	cancel context.CancelFunc
	done   chan struct{}

	sess       driver.Session
	from       driver.Conn
	autoCommit bool
	open       *openResult
}

// openResult is the last result of a run, kept for Fetch.
type openResult struct {
	cur    *cursor
	rows   int64
	script string // for error positions
	stmt   driver.Statement
}

var errBusy = errors.New("tab is busy")

func (s *Service) tab(id string) (*tab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tabs[id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown tab %q", id))
	}
	return t, nil
}

// begin marks the tab busy and returns a context that Cancel and CloseTab
// cancel; release must be called when the operation ends.
func (t *tab) begin(ctx context.Context) (context.Context, func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, nil, connect.NewError(connect.CodeNotFound, errors.New("tab is closed"))
	}
	if t.busy {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errBusy)
	}
	ctx, cancel := context.WithCancel(ctx)
	t.busy, t.cancel, t.done = true, cancel, make(chan struct{})
	return ctx, func() {
		cancel()
		t.mu.Lock()
		closed, done := t.closed, t.done
		t.busy, t.cancel, t.done = false, nil, nil
		t.mu.Unlock()
		if closed {
			t.closeSession()
		}
		close(done)
	}, nil
}

// close marks the tab closed. An idle tab is cleaned up at once; a busy one
// is canceled and cleans up when its operation ends, signalled by done.
func (t *tab) close() (done chan struct{}) {
	t.mu.Lock()
	t.closed = true
	busy := t.busy
	if busy {
		t.cancel()
		done = t.done
	}
	t.mu.Unlock()
	if !busy {
		t.closeSession()
	}
	return done
}

func (t *tab) target() (string, driver.Scope) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connID, t.scope
}

// discard closes the result kept for Fetch, if any.
func (t *tab) discard() {
	if t.open != nil {
		t.open.cur.close()
		t.open = nil
	}
}

func (t *tab) closeSession() {
	t.discard()
	if t.sess != nil {
		if err := t.sess.Close(); err != nil {
			slog.Debug("query: close session", "err", err)
		}
		t.sess, t.from = nil, nil
	}
}

// session returns the tab's session, opening it on first use and reopening it
// when the connection was replaced or the session died.
func (s *Service) session(ctx context.Context, t *tab) (driver.Session, driver.Config, error) {
	connID, scope := t.target()
	conn, cfg, err := s.live.Live(connID)
	if err != nil {
		return nil, cfg, service.LiveError(err)
	}
	if t.sess != nil {
		if t.from == conn && alive(t.sess) {
			return t.sess, cfg, nil
		}
		t.closeSession()
	}
	sess, err := conn.NewSession(ctx, scope)
	if err != nil {
		return nil, cfg, connect.NewError(connect.CodeUnavailable, err)
	}
	t.sess, t.from, t.autoCommit = sess, conn, true
	return sess, cfg, nil
}

func alive(s driver.Session) bool {
	a, ok := s.(interface{ Alive() bool })
	return !ok || a.Alive()
}

func (t *tab) txState() *quarryv1.TxState {
	tr, ok := t.sess.(driver.Transactional)
	if !ok {
		return &quarryv1.TxState{}
	}
	return &quarryv1.TxState{Supported: true, AutoCommit: t.autoCommit, InTx: tr.InTx()}
}

func (s *Service) languageOf(connID string) (string, error) {
	cfg, ok := s.live.Config(connID)
	if !ok {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown connection %q", connID))
	}
	lang, ok := s.language(cfg.Kind)
	if !ok {
		return "", connect.NewError(connect.CodeUnimplemented, fmt.Errorf("no driver for %q", cfg.Kind))
	}
	return string(lang), nil
}

func (s *Service) OpenTab(_ context.Context, req *connect.Request[quarryv1.OpenTabRequest]) (*connect.Response[quarryv1.OpenTabResponse], error) {
	lang, err := s.languageOf(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	t := &tab{connID: req.Msg.ConnId, scope: service.ScopeOf(req.Msg.Scope), autoCommit: true}
	s.mu.Lock()
	s.tabs[id] = t
	s.mu.Unlock()
	return connect.NewResponse(&quarryv1.OpenTabResponse{TabId: id, Language: lang}), nil
}

func (s *Service) Rebind(ctx context.Context, req *connect.Request[quarryv1.RebindRequest]) (*connect.Response[quarryv1.RebindResponse], error) {
	t, err := s.tab(req.Msg.TabId)
	if err != nil {
		return nil, err
	}
	lang, err := s.languageOf(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	_, release, err := t.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	t.closeSession()
	t.mu.Lock()
	t.connID, t.scope = req.Msg.ConnId, service.ScopeOf(req.Msg.Scope)
	t.mu.Unlock()
	return connect.NewResponse(&quarryv1.RebindResponse{Language: lang}), nil
}

func (s *Service) CloseTab(_ context.Context, req *connect.Request[quarryv1.CloseTabRequest]) (*connect.Response[quarryv1.CloseTabResponse], error) {
	s.mu.Lock()
	t, ok := s.tabs[req.Msg.TabId]
	delete(s.tabs, req.Msg.TabId)
	s.mu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown tab %q", req.Msg.TabId))
	}
	t.close()
	return connect.NewResponse(&quarryv1.CloseTabResponse{}), nil
}

func (s *Service) Cancel(_ context.Context, req *connect.Request[quarryv1.CancelRequest]) (*connect.Response[quarryv1.CancelResponse], error) {
	t, err := s.tab(req.Msg.TabId)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.busy {
		t.cancel()
	}
	t.mu.Unlock()
	return connect.NewResponse(&quarryv1.CancelResponse{}), nil
}

func (s *Service) Analyze(_ context.Context, req *connect.Request[quarryv1.AnalyzeRequest]) (*connect.Response[quarryv1.AnalyzeResponse], error) {
	t, err := s.tab(req.Msg.TabId)
	if err != nil {
		return nil, err
	}
	connID, _ := t.target()
	cfg, ok := s.live.Config(connID)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown connection %q", connID))
	}
	sp := splitterFor(cfg.Kind)
	script := req.Msg.Script
	stmts := sp.split(script)
	res := &quarryv1.AnalyzeResponse{Current: -1, Statements: make([]*quarryv1.StatementRange, len(stmts))}
	conv := utf16Conv{s: script}
	for i, st := range stmts {
		res.Statements[i] = rangeOf(&conv, st)
	}
	if cur, ok := sp.at(stmts, script, service.ByteOffset(script, int(req.Msg.Cursor))); ok {
		res.Current = int32(indexOf(stmts, cur))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) SetAutoCommit(ctx context.Context, req *connect.Request[quarryv1.SetAutoCommitRequest]) (*connect.Response[quarryv1.SetAutoCommitResponse], error) {
	on := req.Msg.On
	tx, err := s.tx(ctx, req.Msg.TabId, false, func(ctx context.Context, t *tab, tr driver.Transactional) error {
		if err := tr.SetAutoCommit(ctx, on); err != nil {
			return err
		}
		t.autoCommit = on
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.SetAutoCommitResponse{Tx: tx}), nil
}

func (s *Service) Commit(ctx context.Context, req *connect.Request[quarryv1.CommitRequest]) (*connect.Response[quarryv1.CommitResponse], error) {
	tx, err := s.tx(ctx, req.Msg.TabId, true, func(ctx context.Context, _ *tab, tr driver.Transactional) error {
		return tr.Commit(ctx)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.CommitResponse{Tx: tx}), nil
}

func (s *Service) Rollback(ctx context.Context, req *connect.Request[quarryv1.RollbackRequest]) (*connect.Response[quarryv1.RollbackResponse], error) {
	tx, err := s.tx(ctx, req.Msg.TabId, true, func(ctx context.Context, _ *tab, tr driver.Transactional) error {
		return tr.Rollback(ctx)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&quarryv1.RollbackResponse{Tx: tx}), nil
}

// tx runs a transaction-control operation on the tab's session, opening it
// if needed. Sessions without transactions report supported=false.
func (s *Service) tx(ctx context.Context, tabID string, discard bool, op func(context.Context, *tab, driver.Transactional) error) (*quarryv1.TxState, error) {
	t, err := s.tab(tabID)
	if err != nil {
		return nil, err
	}
	ctx, release, err := t.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if discard {
		t.discard()
	}
	sess, _, err := s.session(ctx, t)
	if err != nil {
		return nil, err
	}
	tr, ok := sess.(driver.Transactional)
	if !ok {
		return &quarryv1.TxState{}, nil
	}
	if err := op(ctx, t, tr); err != nil {
		return nil, connect.NewError(connect.CodeAborted, err)
	}
	return t.txState(), nil
}
