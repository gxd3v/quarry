package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/sqlsafe"
)

// session is one editor tab's dedicated connection: a private go-redis
// client whose single connection is pinned (go-redis Conn) so SELECT,
// MULTI and WATCH state persist between commands. On a Cluster there is no
// single connection; MULTI … EXEC is then emulated client-side and sent as
// one transactional pipeline at EXEC (all keys must hash to one slot).
//
// Like go-redis' Conn it is not safe for concurrent use, except
// DrainMessages, InTx and Alive, which may be called from any goroutine.
type session struct {
	c       *conn
	cl      goredis.UniversalClient
	rc      *goredis.Conn // nil on a Cluster
	scopeDB int
	db      int // current database, follows SELECT

	autoCommit bool
	inTx       atomic.Bool
	queue      [][]string // Cluster MULTI emulation
	dead       atomic.Bool

	connMu sync.Mutex
	conns  []*killConn

	msgMu sync.Mutex
	msgs  []driver.Message
}

var (
	_ driver.Session       = (*session)(nil)
	_ driver.MessageSource = (*session)(nil)
	_ driver.Transactional = (*session)(nil)
)

func (c *conn) NewSession(ctx context.Context, scope driver.Scope) (driver.Session, error) {
	if c.closed.Load() {
		return nil, errClosed
	}
	db, err := c.dbIndex(scope.Database)
	if err != nil {
		return nil, err
	}
	if c.cluster && db != 0 {
		return nil, errors.New("Redis Cluster only has database 0")
	}
	s := &session{c: c, scopeDB: db, db: db, autoCommit: true}
	o := c.opts
	o.DB = db
	o.PoolSize = 1
	if c.cluster {
		o.PoolSize = 2
	}
	o.MinIdleConns = 0
	o.ConnMaxIdleTime = -1
	o.MaxRetries = -1 // never resend a command the server may have run
	o.ReadTimeout = -1
	o.WriteTimeout = -1
	o.Dialer = c.dialer(s.track)
	s.cl = goredis.NewUniversalClient(&o)
	if !c.cluster {
		s.rc = s.cl.(*goredis.Client).Conn()
	}
	pctx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, o.DialTimeout+5*time.Second)
		defer cancel()
	}
	var perr error
	if s.rc != nil {
		perr = s.rc.Ping(pctx).Err()
	} else {
		perr = s.cl.Ping(pctx).Err()
	}
	if perr != nil {
		s.Close()
		return nil, connectError(perr, c.where())
	}
	return s, nil
}

func (s *session) track(k *killConn) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	live := s.conns[:0]
	for _, x := range s.conns {
		if !x.killed.Load() {
			live = append(live, x)
		}
	}
	s.conns = append(live, k)
}

// abort interrupts whatever the session's connections are waiting for.
func (s *session) abort() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	for _, k := range s.conns {
		k.kill()
	}
	s.conns = nil
}

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

func (s *session) Split(script string) []driver.Statement { return Split(script) }

// unsupported lists commands that take over the connection.
var unsupported = map[string]string{
	"MONITOR":      "MONITOR is not supported yet; use the Shell (redis-cli)",
	"SUBSCRIBE":    "SUBSCRIBE is not supported yet; use the Shell (redis-cli)",
	"PSUBSCRIBE":   "PSUBSCRIBE is not supported yet; use the Shell (redis-cli)",
	"SSUBSCRIBE":   "SSUBSCRIBE is not supported yet; use the Shell (redis-cli)",
	"SYNC":         "SYNC is not supported",
	"PSYNC":        "PSYNC is not supported",
	"QUIT":         "QUIT is not supported: close the tab to end the session",
	"CLIENT REPLY": "CLIENT REPLY is not supported: every command must get a reply",
}

func (s *session) Execute(ctx context.Context, stmt driver.Statement) (driver.Result, error) {
	stmts := Split(stmt.Text)
	if len(stmts) == 0 {
		return &result{}, nil
	}
	if len(stmts) > 1 {
		return nil, &driver.QueryError{Msg: "one command per statement: put each command on its own line", Position: stmts[1].Offset}
	}
	line := stmts[0].Text
	args, err := Fields(line)
	if err != nil {
		return nil, &driver.QueryError{Msg: err.Error(), Position: stmts[0].Offset}
	}
	cmd, full := verb(args)
	if msg, ok := unsupported[full]; ok {
		return nil, errors.New(msg)
	}
	if msg, ok := unsupported[cmd]; ok {
		return nil, errors.New(msg)
	}
	if s.c.readOnly && sqlsafe.IsWrite(driver.LangRedis, line) {
		return nil, fmt.Errorf("connection is read-only: %s is not allowed", cmd)
	}
	if s.rc == nil {
		return s.executeCluster(ctx, args, cmd)
	}
	if err := s.revive(ctx); err != nil {
		return nil, err
	}

	if !s.autoCommit && !s.inTx.Load() && opensTx(cmd) && sqlsafe.IsWrite(driver.LangRedis, line) {
		if _, err := s.do(ctx, []string{"MULTI"}); err != nil {
			return nil, s.fail(ctx, err)
		}
		s.inTx.Store(true)
	}

	v, err := s.do(ctx, args)
	switch cmd {
	case "EXEC", "DISCARD":
		if err == nil || isServerError(err) {
			s.inTx.Store(false)
		}
	case "RESET":
		if err == nil {
			s.inTx.Store(false)
			s.db = 0
		}
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	switch cmd {
	case "MULTI":
		s.inTx.Store(true)
	case "SELECT":
		if n, err := strconv.Atoi(args[len(args)-1]); err == nil && !s.inTx.Load() {
			s.db = n
		}
	case "EXEC":
		if v == nil {
			s.message("WARNING", "Transaction aborted: a WATCHed key was modified")
		}
	}
	return s.result(args, v), nil
}

func (s *session) result(args []string, v any) *result {
	t := tabulate(args, v)
	for _, m := range t.msgs {
		s.message(m.Severity, m.Text)
	}
	return &result{cols: t.cols, rows: t.rows}
}

// opensTx reports whether manual-commit mode should send MULTI before cmd.
func opensTx(cmd string) bool {
	switch cmd {
	case "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH", "SELECT", "RESET", "AUTH", "HELLO":
		return false
	}
	return true
}

func isServerError(err error) bool {
	_, ok := errors.AsType[goredis.Error](err)
	return ok && err != goredis.Nil
}

// do runs one command; a nil reply is returned as (nil, nil). Cancelling
// ctx aborts the wait by breaking the connection (Redis cannot cancel a
// running command); the session reconnects on its next use.
func (s *session) do(ctx context.Context, args []string) (any, error) {
	a := make([]any, len(args))
	for i, x := range args {
		a[i] = x
	}
	stop := context.AfterFunc(ctx, s.abort)
	defer stop()
	var cmd *goredis.Cmd
	if s.rc != nil {
		cmd = s.rc.Do(context.WithoutCancel(ctx), a...)
	} else {
		cmd = s.cl.Do(context.WithoutCancel(ctx), a...)
	}
	v, err := cmd.Result()
	if err == goredis.Nil {
		return nil, nil
	}
	return v, err
}

// fail converts err; transport errors mark the connection dead.
func (s *session) fail(ctx context.Context, err error) error {
	if isServerError(err) {
		return queryError(err)
	}
	if s.rc != nil {
		s.dead.Store(true)
		s.inTx.Store(false)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("command interrupted (%w); the connection will be re-established", ctx.Err())
	}
	return &friendlyError{msg: "connection lost: " + strings.TrimPrefix(err.Error(), "redis: "), err: err}
}

// revive replaces a broken pinned connection, restoring the selected
// database; an open transaction is lost.
func (s *session) revive(ctx context.Context) error {
	if !s.dead.Load() {
		return nil
	}
	s.rc.Close()
	s.rc = s.cl.(*goredis.Client).Conn()
	if s.db != s.scopeDB {
		if err := s.rc.Select(ctx, s.db).Err(); err != nil {
			return s.fail(ctx, err)
		}
	} else if err := s.rc.Ping(ctx).Err(); err != nil {
		return s.fail(ctx, err)
	}
	s.dead.Store(false)
	s.message("WARNING", "Reconnected: the previous connection was lost (any open MULTI was discarded)")
	return nil
}

// executeCluster runs a command on a Cluster session, emulating MULTI.
func (s *session) executeCluster(ctx context.Context, args []string, cmd string) (driver.Result, error) {
	switch cmd {
	case "WATCH", "UNWATCH":
		return nil, fmt.Errorf("%s is not supported on Redis Cluster connections", cmd)
	case "MULTI":
		if s.inTx.Load() {
			return nil, &driver.QueryError{Msg: "MULTI calls can not be nested", Code: "ERR", Position: -1}
		}
		s.inTx.Store(true)
		s.queue = nil
		return s.result(args, "OK"), nil
	case "DISCARD":
		if !s.inTx.Load() {
			return nil, &driver.QueryError{Msg: "DISCARD without MULTI", Code: "ERR", Position: -1}
		}
		s.inTx.Store(false)
		s.queue = nil
		return s.result(args, "OK"), nil
	case "EXEC":
		if !s.inTx.Load() {
			return nil, &driver.QueryError{Msg: "EXEC without MULTI", Code: "ERR", Position: -1}
		}
		v, err := s.execQueue(ctx)
		if err != nil {
			return nil, err
		}
		return s.result(args, v), nil
	}
	if !s.inTx.Load() && !s.autoCommit && opensTx(cmd) && sqlsafe.IsWrite(driver.LangRedis, command(args...)) {
		s.inTx.Store(true)
		s.queue = nil
	}
	if s.inTx.Load() {
		s.queue = append(s.queue, args)
		return s.result(args, "QUEUED"), nil
	}
	v, err := s.do(ctx, args)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return s.result(args, v), nil
}

// execQueue sends the queued Cluster commands as one MULTI/EXEC pipeline
// and returns EXEC's reply: one element per command, errors inline.
func (s *session) execQueue(ctx context.Context) ([]any, error) {
	q := s.queue
	s.queue = nil
	s.inTx.Store(false)
	if len(q) == 0 {
		return []any{}, nil
	}
	stop := context.AfterFunc(ctx, s.abort)
	defer stop()
	cmds, err := s.cl.TxPipelined(context.WithoutCancel(ctx), func(p goredis.Pipeliner) error {
		for _, args := range q {
			a := make([]any, len(args))
			for i, x := range args {
				a[i] = x
			}
			p.Do(ctx, a...)
		}
		return nil
	})
	if err != nil && !isServerError(err) {
		return nil, s.fail(ctx, err)
	}
	out := make([]any, len(cmds))
	for i, c := range cmds {
		cmd, ok := c.(*goredis.Cmd)
		switch {
		case c.Err() == goredis.Nil:
			out[i] = nil
		case c.Err() != nil:
			out[i] = c.Err()
		case ok:
			out[i] = cmd.Val()
		}
	}
	return out, nil
}

func (s *session) InTx() bool { return s.inTx.Load() }

// SetAutoCommit switches modes. Redis has no implicit transactions, so
// manual-commit mode is emulated: the first write sends MULTI (reads run
// directly until then; once MULTI is open, everything is queued), Commit
// sends EXEC and Rollback DISCARD. Turning autocommit back on commits.
func (s *session) SetAutoCommit(ctx context.Context, on bool) error {
	if on && !s.autoCommit {
		if err := s.Commit(ctx); err != nil {
			return err
		}
	}
	s.autoCommit = on
	return nil
}

// Commit sends EXEC. Per-command failures inside the transaction do not
// undo the others (Redis semantics); they are reported as messages and as
// the returned error.
func (s *session) Commit(ctx context.Context) error {
	if !s.inTx.Load() {
		return nil
	}
	var (
		v   any
		err error
	)
	if s.rc == nil {
		v, err = s.execQueue(ctx)
	} else {
		v, err = s.do(ctx, []string{"EXEC"})
		if err == nil || isServerError(err) {
			s.inTx.Store(false)
		}
		if err != nil {
			return s.fail(ctx, err)
		}
	}
	if err != nil {
		return err
	}
	if v == nil {
		return errors.New("transaction aborted: a WATCHed key was modified")
	}
	a, _ := v.([]any)
	var failed []string
	for _, e := range a {
		if e, ok := e.(error); ok {
			failed = append(failed, e.Error())
			s.message("ERROR", e.Error())
		}
	}
	s.message("INFO", fmt.Sprintf("EXEC: %d command(s) executed", len(a)))
	if len(failed) > 0 {
		return fmt.Errorf("committed with errors: %d of %d commands failed: %s", len(failed), len(a), failed[0])
	}
	return nil
}

func (s *session) Rollback(ctx context.Context) error {
	if !s.inTx.Load() {
		return nil
	}
	if s.rc == nil {
		s.queue = nil
		s.inTx.Store(false)
		return nil
	}
	_, err := s.do(ctx, []string{"DISCARD"})
	if err == nil || isServerError(err) {
		s.inTx.Store(false)
	}
	if err != nil {
		return s.fail(ctx, err)
	}
	return nil
}

func (s *session) Close() error {
	var errs []error
	if s.rc != nil {
		errs = append(errs, s.rc.Close())
	}
	errs = append(errs, s.cl.Close())
	for i, e := range errs {
		if e == goredis.ErrClosed {
			errs[i] = nil
		}
	}
	return errors.Join(errs...)
}
