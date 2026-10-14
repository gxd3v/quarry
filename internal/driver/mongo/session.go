package mongo

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/sqlsafe"
)

var errNoTx = errors.New("transactions need a replica set or a sharded cluster; this server is a standalone mongod")

// session is one editor tab. The client multiplexes every tab on its pool,
// so a session is state rather than a socket: its current database
// (changed by `use`), and a server session (lsid) that gives the tab causal
// consistency and its own transaction.
//
// Cancelling ctx during Execute or Next makes the Go driver return at once;
// the session then kills its server-side operations (found by lsid with
// $currentOp), so nothing keeps running, and stays usable. A cancelled
// statement inside a transaction rolls the transaction back.
//
// Like mongo.Session it is not safe for concurrent use, except
// DrainMessages, InTx and Alive.
type session struct {
	c          *conn
	db         string
	ms         *mongo.Session
	lsid       bson.RawValue // ms.ID()'s id, to find our operations
	autoCommit bool
	inTx       atomic.Bool
	closed     atomic.Bool
	lost       atomic.Bool
	cur        driver.Result

	msgMu sync.Mutex
	msgs  []driver.Message
}

var (
	_ driver.Session       = (*session)(nil)
	_ driver.Transactional = (*session)(nil)
	_ driver.MessageSource = (*session)(nil)
)

func (c *conn) NewSession(ctx context.Context, scope driver.Scope) (driver.Session, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	ms, err := c.client.StartSession(options.Session().SetCausalConsistency(true))
	if err != nil {
		return nil, queryError(err)
	}
	s := &session{c: c, db: cmp.Or(scope.Database, c.defaultDB), ms: ms, autoCommit: true}
	if id, err := ms.ID().LookupErr("id"); err == nil {
		s.lsid = bson.RawValue{Type: id.Type, Value: slices.Clone(id.Value)}
	}
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

// Execute parses and runs one statement (see run for what is supported).
// Documents stream from a server cursor; everything else returns a small
// in-memory result. On a read-only connection statements that may write
// (sqlsafe.IsWrite) are refused before they reach the server.
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
		verb := cmp.Or(sqlsafe.Analyze(driver.LangMongo, stmt.Text).Verb, "this statement")
		pos := -1
		if len(st.calls) > 0 {
			pos = st.calls[0].pos
		}
		return nil, &driver.QueryError{Msg: "connection is read-only: " + verb + " is not allowed", Position: pos}
	}
	var res driver.Result
	err = s.interruptible(ctx, func(ctx context.Context) error {
		var err error
		res, err = s.run(ctx, st)
		return err
	})
	if err != nil {
		s.afterError(ctx, err)
		return nil, queryError(err)
	}
	s.lost.Store(false)
	s.cur = res
	return res, nil
}

// interruptible runs fn; if ctx is cancelled meanwhile, the session's
// server-side operations are killed before interruptible returns, so a kill
// can never hit the next statement.
func (s *session) interruptible(ctx context.Context, fn func(context.Context) error) error {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		s.killOps()
	})
	defer func() {
		if !stop() {
			<-done
		}
	}()
	return fn(ctx)
}

// killOps kills the operations running under the session's lsid. Errors
// are ignored: the operation may have finished, or the user may lack the
// privilege (everyone may kill their own operations).
func (s *session) killOps() {
	if s.lsid.Type == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	admin := s.c.client.Database("admin")
	var reply struct {
		InProg []struct {
			OpID bson.RawValue `bson:"opid"`
		} `bson:"inprog"`
	}
	cmd := bson.D{{Key: "currentOp", Value: 1}, {Key: "$ownOps", Value: true}, {Key: "lsid.id", Value: s.lsid}}
	if err := admin.RunCommand(ctx, cmd).Decode(&reply); err != nil {
		return
	}
	for _, op := range reply.InProg {
		admin.RunCommand(ctx, bson.D{{Key: "killOp", Value: 1}, {Key: "op", Value: op.OpID}})
	}
}

// afterError keeps the session consistent after a failed operation: a
// transaction the server gave up on (or whose statement was cancelled) is
// rolled back, and network failures mark the session as not alive.
func (s *session) afterError(ctx context.Context, err error) {
	if mongo.IsNetworkError(err) && ctx.Err() == nil {
		s.lost.Store(true)
	}
	if !s.ms.TransactionRunning() {
		return
	}
	le, labeled := errors.AsType[mongo.LabeledError](err)
	transient := labeled && le.HasErrorLabel("TransientTransactionError")
	if ctx.Err() == nil && !transient {
		return
	}
	actx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	s.ms.AbortTransaction(actx)
	cancel()
	s.inTx.Store(false)
	s.message("WARNING", "the transaction was aborted and rolled back")
}

// opCtx returns the context an operation runs with. In manual-commit mode,
// operations allowed in transactions (txable) run in the session's
// transaction, which starts lazily; other operations (DDL, commands) run
// outside it, without the session. Otherwise operations use the session
// for causal consistency.
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

// SetAutoCommit switches modes. Manual commit needs a replica set or
// mongos; the transaction starts with the first CRUD statement. Turning
// autocommit back on commits an open transaction.
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
	if err != nil && s.ms.TransactionRunning() {
		// A failed commit leaves the transaction unusable.
		actx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		s.ms.AbortTransaction(actx)
		cancel()
	}
	s.inTx.Store(false)
	return queryError(err)
}

func (s *session) Rollback(ctx context.Context) error {
	s.closeCur()
	if !s.ms.TransactionRunning() {
		s.inTx.Store(false)
		return nil
	}
	err := s.ms.AbortTransaction(ctx)
	s.inTx.Store(false)
	return queryError(err)
}

func (s *session) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.closeCur()
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	s.ms.EndSession(ctx) // aborts an open transaction
	s.inTx.Store(false)
	return nil
}
