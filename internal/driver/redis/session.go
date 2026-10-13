package redis

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/sqlsafe"
)

const (
	// interruptGrace is how long a canceled command gets to return (after
	// CLIENT UNBLOCK, for blocking commands) before its socket is broken.
	interruptGrace = time.Second
	// idleCheck: a pinned connection idle for longer is pinged before use,
	// so a server-side idle timeout reconnects transparently.
	idleCheck = time.Minute
)

// session is one editor tab's dedicated connection: a private go-redis
// client whose single connection is pinned (go-redis EphemeralConn) so
// SELECT, MULTI and WATCH state persist between commands and never reach
// another tab.
//
// A Cluster has no single connection: the session gets a private cluster
// client, SELECT is not available, WATCH is refused and MULTI … EXEC is
// emulated client-side, sent as one transactional pipeline at EXEC (all keys
// must hash to one slot).
//
// Cancelling a command first tries CLIENT UNBLOCK (which ends blocking
// commands like BLPOP cleanly, keeping the connection); a command that is
// still running after interruptGrace has its socket broken, and the next
// command reconnects transparently, restoring the selected database.
//
// Like go-redis' Conn it is not safe for concurrent use, except
// DrainMessages, InTx and Alive, which may be called from any goroutine.
type session struct {
	c       *conn
	cl      goredis.UniversalClient
	rc      *goredis.Conn // pinned connection; nil on a Cluster
	cluster bool
	scopeDB int
	db      int // current database, follows SELECT
	// queuedDB is a SELECT queued inside MULTI, applied by EXEC; -1 if none.
	queuedDB int

	autoCommit bool
	inTx       atomic.Bool
	watching   bool
	queue      [][]string // Cluster MULTI emulation
	stale      bool       // the pinned connection broke: reconnect before the next command
	lost       atomic.Bool
	id         atomic.Int64 // CLIENT ID of the pinned connection, 0 if unknown
	lastUse    time.Time
	cur        *result

	connMu  sync.Mutex
	conns   []*killConn
	killing bool // a cancelled command is being aborted: kill new connections too

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
	s := &session{c: c, cluster: c.cluster, scopeDB: db, db: db, queuedDB: -1, autoCommit: true}
	o := c.opts
	o.DB = db
	o.PoolSize = 1
	if c.cluster {
		o.PoolSize = 2 // per node
	}
	o.MinIdleConns = 0
	o.ConnMaxIdleTime = -1
	o.MaxRetries = -1 // never resend a command the server may have run
	o.ReadTimeout = -1
	o.WriteTimeout = -1
	o.Dialer = c.dialer(s.track)
	s.cl = goredis.NewUniversalClient(&o)
	if err := s.connect(ctx); err != nil {
		s.Close()
		return nil, connectError(err, c.where())
	}
	return s, nil
}

// connect pins a new connection (Cluster: checks a node answers) and
// restores the selected database.
func (s *session) connect(ctx context.Context) error {
	ctx, cancel := s.c.bound(ctx)
	defer cancel()
	s.lastUse = time.Now()
	cl, ok := s.cl.(*goredis.Client)
	if !ok {
		return s.cl.Ping(ctx).Err()
	}
	rc := cl.EphemeralConn()
	id, err := rc.ClientID(ctx).Result()
	if err != nil {
		// CLIENT ID may be denied by ACLs; cancellation then only breaks
		// the socket.
		if !isServerError(err) {
			rc.Close()
			return err
		}
		id = 0
		if err := rc.Ping(ctx).Err(); err != nil {
			rc.Close()
			return err
		}
	}
	if s.db != s.scopeDB {
		if err := rc.Select(ctx, s.db).Err(); err != nil {
			rc.Close()
			return err
		}
	}
	s.rc = rc
	s.id.Store(id)
	return nil
}

func (s *session) track(k *killConn) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.killing {
		k.kill() // a retry of the aborted command must not block again
	}
	s.conns = append(s.conns, k)
}

// abort interrupts whatever the session's connections are waiting for.
func (s *session) abort() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.killing = true
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

func unsupportedMsg(cmd, full string) (string, bool) {
	if msg, ok := unsupported[full]; ok {
		return msg, true
	}
	msg, ok := unsupported[cmd]
	return msg, ok
}

// Execute runs one command line. In manual-commit mode the first write
// opens MULTI (see SetAutoCommit).
func (s *session) Execute(ctx context.Context, stmt driver.Statement) (driver.Result, error) {
	s.closeCur()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stmts := Split(stmt.Text)
	if len(stmts) == 0 {
		return &result{}, nil
	}
	if len(stmts) > 1 {
		return nil, &driver.QueryError{Msg: "one command per statement: put each command on its own line", Position: stmts[1].Offset}
	}
	line := stmts[0]
	args, err := Fields(line.Text)
	if err != nil {
		pos := line.Offset
		if se, ok := errors.AsType[*syntaxError](err); ok {
			pos += se.pos
		}
		return nil, &driver.QueryError{Msg: err.Error(), Code: "ERR", Position: pos}
	}
	cmd, full := verb(args)
	if msg, ok := unsupportedMsg(cmd, full); ok {
		return nil, errors.New(msg)
	}
	if s.c.readOnly && sqlsafe.IsWrite(driver.LangRedis, line.Text) {
		return nil, fmt.Errorf("connection is read-only: %s is not allowed", full)
	}
	if s.cluster {
		return s.executeCluster(ctx, args, cmd)
	}
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if !s.autoCommit && !s.inTx.Load() && opensTx(cmd) && sqlsafe.IsWrite(driver.LangRedis, line.Text) {
		if _, err := s.do(ctx, []string{"MULTI"}); err != nil {
			return nil, s.fail(ctx, err)
		}
		s.inTx.Store(true)
	}
	v, err := s.do(ctx, args)
	s.follow(cmd, args, v, err)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	if cmd == "EXEC" && v == nil {
		s.message("WARNING", "Transaction aborted: a WATCHed key was modified")
	}
	return s.result(args, v)
}

// follow tracks the connection state a command changed.
func (s *session) follow(cmd string, args []string, v any, err error) {
	replied := err == nil || isServerError(err)
	switch cmd {
	case "MULTI":
		if err == nil {
			s.inTx.Store(true)
		}
	case "EXEC":
		if replied {
			if err == nil && v != nil && s.queuedDB >= 0 {
				s.db = s.queuedDB
			}
			s.endTx()
		}
	case "DISCARD":
		if err == nil {
			s.endTx()
		}
	case "WATCH":
		if err == nil && !s.inTx.Load() {
			s.watching = true
		}
	case "UNWATCH":
		if err == nil && !s.inTx.Load() {
			s.watching = false
		}
	case "SELECT":
		n, perr := strconv.Atoi(args[len(args)-1])
		switch {
		case err != nil || perr != nil:
		case s.inTx.Load():
			s.queuedDB = n
		default:
			s.db = n
		}
	case "RESET":
		if err == nil {
			s.endTx()
			s.db = 0
		}
	}
}

func (s *session) endTx() {
	s.inTx.Store(false)
	s.watching = false
	s.queuedDB = -1
}

// opensTx reports whether manual-commit mode should send MULTI before cmd.
func opensTx(cmd string) bool {
	switch cmd {
	case "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH", "SELECT", "RESET", "AUTH", "HELLO":
		return false
	}
	return true
}

// ready reconnects a broken pinned connection, and pings one that has been
// idle for a while (unless it holds MULTI or WATCH state, which a reconnect
// would silently drop) so a server-side timeout is healed before the user's
// command is sent.
func (s *session) ready(ctx context.Context) error {
	if !s.stale && !s.inTx.Load() && !s.watching && time.Since(s.lastUse) > idleCheck {
		if _, err := s.do(ctx, []string{"PING"}); err != nil && !isServerError(err) {
			s.stale = true
			if ctx.Err() != nil {
				return fmt.Errorf("command canceled: %w", context.Cause(ctx))
			}
		}
	}
	if !s.stale {
		return nil
	}
	if s.rc != nil {
		s.rc.Close()
		s.rc = nil
	}
	s.connMu.Lock()
	s.conns = nil
	s.connMu.Unlock()
	if err := s.connect(ctx); err != nil {
		s.lost.Store(true)
		return connectError(err, s.c.where())
	}
	s.stale = false
	s.lost.Store(false)
	s.message("WARNING", "Reconnected to the server: the previous connection was lost")
	return nil
}

// doer is what commands are sent through: the pinned connection, the
// cluster client or one cluster node.
type doer interface {
	Do(ctx context.Context, args ...any) *goredis.Cmd
}

var errStale = errors.New("connection lost: run the command again to reconnect")

// do runs one command; a nil reply is returned as (nil, nil).
func (s *session) do(ctx context.Context, args []string) (any, error) {
	if s.cluster {
		return s.doOn(ctx, s.cl, args)
	}
	if s.rc == nil || s.stale {
		return nil, errStale
	}
	return s.doOn(ctx, s.rc, args)
}

// doOn runs one command on d. The command itself ignores ctx: cancelling
// ctx interrupts it (see session), and doOn returns once the interruption
// has settled, so it never races with the next command.
func (s *session) doOn(ctx context.Context, d doer, args []string) (any, error) {
	a := make([]any, len(args))
	for i, x := range args {
		a[i] = x
	}
	var (
		v   any
		err error
	)
	s.interruptible(ctx, func(ctx context.Context) { v, err = d.Do(ctx, a...).Result() })
	s.lastUse = time.Now()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	return v, err
}

// interruptible runs fn with a context that ignores ctx's cancellation;
// cancelling ctx interrupts fn instead (see interrupt). It returns once the
// interruption has settled, and new connections are left alone again.
func (s *session) interruptible(ctx context.Context, fn func(context.Context)) {
	done := make(chan struct{})
	settled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(settled)
		s.interrupt(done)
	})
	fn(context.WithoutCancel(ctx))
	close(done)
	if !stop() {
		<-settled
	}
	s.connMu.Lock()
	s.killing = false
	s.connMu.Unlock()
}

func (s *session) interrupt(done <-chan struct{}) {
	if id := s.id.Load(); id > 0 {
		s.unblock(id)
	}
	select {
	case <-done:
	case <-time.After(interruptGrace):
		s.abort()
	}
}

// unblock sends CLIENT UNBLOCK id ERROR from the metadata connection: a
// blocking command then fails with UNBLOCKED and the session keeps its
// connection. Any other command is not affected.
func (s *session) unblock(id int64) {
	cl, err := s.c.client(s.c.defDB)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), interruptGrace)
	defer cancel()
	cl.ClientUnblockWithError(ctx, id)
}

// fail converts the error of a command sent with ctx.
func (s *session) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil && (!isServerError(err) || strings.HasPrefix(err.Error(), "UNBLOCKED")) {
		if !isServerError(err) {
			s.broke()
		}
		return fmt.Errorf("command canceled: %w", context.Cause(ctx))
	}
	if isServerError(err) {
		return queryError(err)
	}
	if errors.Is(err, errStale) {
		return err
	}
	s.broke()
	msg := strings.TrimPrefix(err.Error(), "redis: ")
	if s.cluster {
		return &friendlyError{msg: "connection lost: " + msg, err: err}
	}
	return &friendlyError{msg: "connection lost: " + msg + " (the next command reconnects)", err: err}
}

// broke marks the pinned connection for replacement; the server drops its
// MULTI and WATCH state with it.
func (s *session) broke() {
	if s.cluster {
		return // go-redis redials cluster nodes by itself
	}
	if s.inTx.Load() || s.watching {
		s.message("WARNING", "The connection was lost: the open transaction (MULTI/WATCH) was discarded")
	}
	s.stale = true
	s.endTx()
}

// result lays out reply v of args, continuing SCAN-family cursors lazily.
func (s *session) result(args []string, v any) (driver.Result, error) {
	t := tabulate(args, v)
	for _, m := range t.msgs {
		s.message(m.Severity, m.Text)
	}
	r := &result{cols: t.cols, rows: t.rows}
	if t.cursor != "" && t.cursor != "0" {
		sc := s.scanner(args, t.cursor, nil)
		r.rows = sc.dedup(t.rows)
		r.more = sc.page
		s.cur = r
	}
	return r, nil
}

func (s *session) closeCur() {
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
}

// executeCluster runs a command on a Cluster session, emulating MULTI.
func (s *session) executeCluster(ctx context.Context, args []string, cmd string) (driver.Result, error) {
	switch cmd {
	case "WATCH", "UNWATCH":
		return nil, fmt.Errorf("%s is not supported on Redis Cluster connections", cmd)
	case "SELECT":
		return nil, errClusterDB
	case "MULTI":
		if s.inTx.Load() {
			return nil, &driver.QueryError{Msg: "MULTI calls can not be nested", Code: "ERR", Position: -1}
		}
		s.inTx.Store(true)
		s.queue = nil
		return s.result(args, "OK")
	case "DISCARD":
		if !s.inTx.Load() {
			return nil, &driver.QueryError{Msg: "DISCARD without MULTI", Code: "ERR", Position: -1}
		}
		s.inTx.Store(false)
		s.queue = nil
		return s.result(args, "OK")
	case "EXEC":
		if !s.inTx.Load() {
			return nil, &driver.QueryError{Msg: "EXEC without MULTI", Code: "ERR", Position: -1}
		}
		v, err := s.execQueue(ctx)
		if err != nil {
			return nil, err
		}
		return s.result(args, v)
	}
	if !s.inTx.Load() && !s.autoCommit && opensTx(cmd) && sqlsafe.IsWrite(driver.LangRedis, command(args...)) {
		s.inTx.Store(true)
		s.queue = nil
	}
	if s.inTx.Load() {
		s.queue = append(s.queue, args)
		return s.result(args, "QUEUED")
	}
	if cmd == "SCAN" {
		return s.clusterScan(ctx, args)
	}
	v, err := s.do(ctx, args)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return s.result(args, v)
}

// clusterScan runs SCAN on every primary in turn: a cursor only means
// something to the node that issued it.
func (s *session) clusterScan(ctx context.Context, args []string) (driver.Result, error) {
	if len(args) < 2 {
		return nil, &driver.QueryError{Msg: "wrong number of arguments for 'scan' command", Code: "ERR", Position: -1}
	}
	nodes, err := s.masters(ctx)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	sc := s.scanner(args, args[1], nodes)
	rows, done, err := sc.page(ctx)
	if err != nil {
		return nil, err
	}
	r := &result{cols: cols("key"), rows: rows}
	if !done {
		r.more = sc.page
		s.cur = r
	}
	return r, nil
}

func (s *session) masters(ctx context.Context) ([]*goredis.Client, error) {
	var (
		mu  sync.Mutex
		out []*goredis.Client
	)
	err := s.cl.(*goredis.ClusterClient).ForEachMaster(ctx, func(_ context.Context, m *goredis.Client) error {
		mu.Lock()
		out = append(out, m)
		mu.Unlock()
		return nil
	})
	slices.SortFunc(out, func(a, b *goredis.Client) int { return cmp.Compare(a.Options().Addr, b.Options().Addr) })
	return out, err
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
	var (
		cmds []goredis.Cmder
		err  error
	)
	s.interruptible(ctx, func(qctx context.Context) {
		cmds, err = s.cl.TxPipelined(qctx, func(p goredis.Pipeliner) error {
			for _, args := range q {
				a := make([]any, len(args))
				for i, x := range args {
					a[i] = x
				}
				p.Do(qctx, a...)
			}
			return nil
		})
	})
	if err != nil && !isServerError(err) {
		return nil, s.fail(ctx, err)
	}
	out := make([]any, len(cmds))
	for i, c := range cmds {
		switch err := c.Err(); {
		case errors.Is(err, goredis.Nil):
			out[i] = nil
		case err != nil:
			out[i] = err
		default:
			if cmd, ok := c.(*goredis.Cmd); ok {
				out[i] = cmd.Val()
			}
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
	s.closeCur()
	if !s.inTx.Load() {
		return nil
	}
	var (
		v   any
		err error
	)
	if s.cluster {
		v, err = s.execQueue(ctx)
	} else {
		v, err = s.do(ctx, []string{"EXEC"})
		s.follow("EXEC", nil, v, err)
		if err != nil {
			err = s.fail(ctx, err)
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
	s.closeCur()
	if !s.inTx.Load() {
		return nil
	}
	if s.cluster {
		s.queue = nil
		s.inTx.Store(false)
		return nil
	}
	_, err := s.do(ctx, []string{"DISCARD"})
	s.follow("DISCARD", nil, nil, err)
	if err != nil {
		return s.fail(ctx, err)
	}
	return nil
}

// SetTTL makes key (in the session's current database) expire after ttl,
// rounded to milliseconds; ttl <= 0 removes the expiry (PERSIST). It refuses
// to run inside a transaction, where it would only be queued.
func (s *session) SetTTL(ctx context.Context, key string, ttl time.Duration) error {
	if s.c.readOnly {
		return errReadOnly
	}
	if s.inTx.Load() {
		return errors.New("a transaction is open: commit or roll back before changing a TTL")
	}
	s.closeCur()
	if !s.cluster {
		if err := s.ready(ctx); err != nil {
			return err
		}
	}
	return setTTL(ctx, func(ctx context.Context, args []string) (any, error) {
		v, err := s.do(ctx, args)
		if err != nil {
			return nil, s.fail(ctx, err)
		}
		return v, nil
	}, key, ttl)
}

func (s *session) Close() error {
	s.closeCur()
	var errs []error
	if s.rc != nil {
		errs = append(errs, s.rc.Close())
		s.rc = nil
	}
	errs = append(errs, s.cl.Close())
	for i, e := range errs {
		if errors.Is(e, goredis.ErrClosed) {
			errs[i] = nil
		}
	}
	s.lost.Store(true)
	return errors.Join(errs...)
}
