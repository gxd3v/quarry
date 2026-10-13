package mongo

import (
	"cmp"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/sqlsafe"
)

var errNoTx = errors.New("transactions require a replica set or sharded cluster; this server is a standalone mongod")

// session is one editor tab. It shares the connection's client but has its
// own current database (changed by `use`) and its own driver session, so
// causal consistency and transactions stay per tab. Like the driver
// session it is not safe for concurrent use, except DrainMessages, InTx
// and Alive.
type session struct {
	c          *conn
	db         string
	ms         *mongo.Session
	autoCommit bool
	inTx       atomic.Bool
	closed     atomic.Bool
	cur        *docResult

	msgMu sync.Mutex
	msgs  []driver.Message
}

var (
	_ driver.Session       = (*session)(nil)
	_ driver.Transactional = (*session)(nil)
	_ driver.MessageSource = (*session)(nil)
	_ driver.Explainer     = (*session)(nil)
)

func (c *conn) NewSession(ctx context.Context, scope driver.Scope) (driver.Session, error) {
	if c.closed.Load() {
		return nil, errClosed
	}
	ms, err := c.client.StartSession(options.Session().SetCausalConsistency(true))
	if err != nil {
		return nil, queryError(err)
	}
	s := &session{c: c, db: cmp.Or(scope.Database, c.defaultDB), ms: ms, autoCommit: true}
	c.sessMu.Lock()
	if c.sessions == nil {
		c.sessions = map[*session]struct{}{}
	}
	c.sessions[s] = struct{}{}
	c.sessMu.Unlock()
	return s, nil
}

func (s *session) Split(script string) []driver.Statement { return Split(script) }

// Database returns the session's current database (changed by `use`).
func (s *session) Database() string { return s.db }

func (s *session) message(sev, text string) {
	s.msgMu.Lock()
	s.msgs = append(s.msgs, driver.Message{Severity: sev, Text: text})
	s.msgMu.Unlock()
}

func (s *session) DrainMessages() []driver.Message {
	s.msgMu.Lock()
	defer s.msgMu.Unlock()
	m := s.msgs
	s.msgs = nil
	return m
}

func (s *session) closeCur() {
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
}

func (s *session) check() error {
	if s.closed.Load() || s.c.closed.Load() {
		return errClosed
	}
	return nil
}

// Execute parses and runs one statement. Documents stream from a server
// cursor; everything else returns a small in-memory result.
func (s *session) Execute(ctx context.Context, stmt driver.Statement) (driver.Result, error) {
	s.closeCur()
	if err := s.check(); err != nil {
		return nil, err
	}
	st, err := parseStatement(stmt.Text)
	if err != nil {
		return nil, queryError(err)
	}
	if s.c.readOnly && sqlsafe.IsWrite(driver.LangMongo, stmt.Text) {
		rep := sqlsafe.Analyze(driver.LangMongo, stmt.Text)
		verb := cmp.Or(rep.Verb, "this statement")
		pos := -1
		if len(st.calls) > 0 {
			pos = st.calls[0].pos
		}
		return nil, &driver.QueryError{Msg: "connection is read-only: " + verb + " is not allowed", Position: pos}
	}
	res, err := s.run(ctx, st)
	if err != nil {
		s.afterError(err)
		return nil, queryError(err)
	}
	if dr, ok := res.(*docResult); ok {
		dr.s = s
		if !dr.done {
			s.cur = dr
		}
	}
	return res, nil
}

// afterError aborts a transaction the server has already given up on, so
// the next statement starts a fresh one.
func (s *session) afterError(err error) {
	if !s.ms.TransactionRunning() {
		return
	}
	var le mongo.LabeledError
	if errors.As(err, &le) && le.HasErrorLabel("TransientTransactionError") {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		s.ms.AbortTransaction(ctx)
		cancel()
		s.inTx.Store(false)
		s.message("WARNING", "the transaction was aborted by the server and rolled back")
	}
}

// opCtx returns the context an operation runs with. In manual-commit mode,
// operations allowed in transactions (txable) run in the session's
// transaction, which starts lazily; other operations (DDL, admin commands)
// run outside it. Otherwise operations use the session for causal
// consistency.
func (s *session) opCtx(ctx context.Context, txable bool) (context.Context, error) {
	if !s.autoCommit && txable {
		if !s.ms.TransactionRunning() {
			if err := s.ms.StartTransaction(); err != nil {
				return nil, err
			}
			s.inTx.Store(true)
		}
		return mongo.NewSessionContext(ctx, s.ms), nil
	}
	if s.ms.TransactionRunning() {
		return ctx, nil
	}
	return mongo.NewSessionContext(ctx, s.ms), nil
}

func (s *session) InTx() bool { return s.inTx.Load() }

// SetAutoCommit switches modes. Manual commit requires a replica set or
// mongos; turning autocommit back on commits an open transaction.
func (s *session) SetAutoCommit(ctx context.Context, on bool) error {
	if err := s.check(); err != nil {
		return err
	}
	if !on && !s.c.replicaSet {
		return errNoTx
	}
	if on && !s.autoCommit {
		if err := s.Commit(ctx); err != nil {
			return err
		}
	}
	s.autoCommit = on
	return nil
}

func (s *session) Commit(ctx context.Context) error {
	s.closeCur()
	if !s.ms.TransactionRunning() {
		s.inTx.Store(false)
		return nil
	}
	err := s.ms.CommitTransaction(ctx)
	s.inTx.Store(s.ms.TransactionRunning())
	if err != nil {
		// A failed commit leaves the transaction unusable: roll it back so
		// the next statement starts clean.
		if s.ms.TransactionRunning() {
			actx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			s.ms.AbortTransaction(actx)
			cancel()
		}
		s.inTx.Store(false)
		return queryError(err)
	}
	s.inTx.Store(false)
	return nil
}

func (s *session) Rollback(ctx context.Context) error {
	s.closeCur()
	if !s.ms.TransactionRunning() {
		s.inTx.Store(false)
		return nil
	}
	err := s.ms.AbortTransaction(ctx)
	s.inTx.Store(false)
	if err != nil {
		return queryError(err)
	}
	return nil
}

func (s *session) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.closeCur()
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if s.ms.TransactionRunning() {
		s.ms.AbortTransaction(ctx)
	}
	s.ms.EndSession(ctx)
	s.c.sessMu.Lock()
	delete(s.c.sessions, s)
	s.c.sessMu.Unlock()
	return nil
}
