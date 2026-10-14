package oracle

import (
	"cmp"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/sqlsafe"
)

const (
	// cancelGrace is how long a cancelled call may take to stop on its own
	// (go-ora sends an in-band break) before ALTER SYSTEM CANCEL SQL is used.
	cancelGrace = 200 * time.Millisecond
	// outputBatch and maxOutputLines bound DBMS_OUTPUT fetching: lines are
	// read in batches, and beyond the cap the rest is discarded.
	outputBatch    = 1000
	maxOutputLines = 10000
)

// session is one editor tab's dedicated server session. Like the go-ora
// connection it is not safe for concurrent use, except DrainMessages, InTx
// and Alive, which may be called from any goroutine.
//
// go-ora's own autocommit is turned off for good: in autocommit mode the
// driver commits from the block it runs after every statement anyway (see
// poll), which also covers CALL, LOCK and anything else that opens a
// transaction, in the same round trip.
type session struct {
	parent     *conn
	link       *link
	autoCommit bool
	inTx       atomic.Bool
	cur        *result
	// ident is "sid,serial#" when the user may cancel through ALTER SYSTEM
	// CANCEL SQL; empty otherwise.
	ident string

	stray   atomic.Bool // an untargeted cancel may still interrupt the next call
	aborted atomic.Bool // the last call's socket was closed after a cancel

	msgMu sync.Mutex
	msgs  []driver.Message
}

var (
	_ driver.Session       = (*session)(nil)
	_ driver.MessageSource = (*session)(nil)
	_ driver.Transactional = (*session)(nil)
)

// initBlock enables DBMS_OUTPUT, positions the session at its schema
// (exact name, else upper-cased) and finds what cancellation needs.
const initBlock = `DECLARE
  s VARCHAR2(128) := :1;
  n NUMBER;
  ser NUMBER;
BEGIN
  DBMS_OUTPUT.ENABLE(NULL);
  IF s IS NOT NULL THEN
    SELECT COUNT(*) INTO n FROM all_users WHERE username = s;
    IF n = 0 THEN
      s := UPPER(s);
    END IF;
    EXECUTE IMMEDIATE 'ALTER SESSION SET CURRENT_SCHEMA = ' || DBMS_ASSERT.ENQUOTE_NAME(s, FALSE);
  END IF;
  BEGIN
    EXECUTE IMMEDIATE 'SELECT serial# FROM v$session WHERE sid = SYS_CONTEXT(''USERENV'', ''SID'')' INTO ser;
  EXCEPTION
    WHEN OTHERS THEN
      ser := NULL;
  END;
  SELECT COUNT(*) INTO n FROM session_privs WHERE privilege = 'ALTER SYSTEM';
  :2 := TO_NUMBER(SYS_CONTEXT('USERENV', 'SID'));
  :3 := ser;
  :4 := n;
END;`

// NewSession opens a dedicated session. scope.Schema becomes the
// CURRENT_SCHEMA; scope.Database is ignored (one PDB per connection).
func (c *conn) NewSession(ctx context.Context, scope driver.Scope) (driver.Session, error) {
	if c.closed.Load() {
		return nil, errClosed
	}
	l, err := c.st.dial(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := l.oc.Begin(); err != nil {
		l.close()
		return nil, err
	}
	var sid, serial, priv int64
	args := []any{scope.Schema, sql.Out{Dest: &sid}, sql.Out{Dest: &serial}, sql.Out{Dest: &priv}}
	if _, err := l.oc.ExecContext(ctx, initBlock, named(args)); err != nil {
		l.close()
		return nil, queryError(err, "")
	}
	s := &session{parent: c, link: l, autoCommit: true}
	if serial > 0 && priv > 0 {
		s.ident = fmt.Sprintf("%d,%d", sid, serial)
	}
	return s, nil
}

func (s *session) addMsg(sev, text string) {
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

// Execute runs one statement as split by Split: SQL without its semicolon,
// PL/SQL with it, or a SQL*Plus command (see sqlplus). A trailing semicolon
// on SQL and a trailing "/" line are dropped, so statements can also be
// passed as typed.
func (s *session) Execute(ctx context.Context, stmt driver.Statement) (driver.Result, error) {
	s.closeCur()
	if !s.link.alive() {
		return nil, errLost
	}
	toks := codeTokens(stmt.Text)
	if len(toks) == 0 {
		return &memResult{}, nil
	}
	if t := toks[0]; t.kind == tokWord && isSQLPlus(toks, 0) || t.kind == tokOther && stmt.Text[t.start] == '@' {
		return s.sqlplus(ctx, stmt.Text, toks)
	}
	if err := s.checkWrite(stmt.Text, toks); err != nil {
		return nil, err
	}
	unit, _ := unitKind(toks)
	return s.run(ctx, execText(stmt.Text, toks, unit), toks, unit)
}

// checkWrite refuses statements that may write on a read-only connection.
// Oracle has no session-level read-only mode, so this is client-side; ALTER
// SESSION is allowed.
func (s *session) checkWrite(text string, toks []token) error {
	if !s.parent.readOnly {
		return nil
	}
	if w := codeWords(toks, 2); len(w) == 2 && w[0] == "alter" && w[1] == "session" {
		return nil
	}
	if !sqlsafe.IsWrite(driver.LangSQL, text) {
		return nil
	}
	verb := cmp.Or(sqlsafe.Analyze(driver.LangSQL, text).Verb, "this statement")
	return fmt.Errorf("connection is read-only: %s is not allowed", verb)
}

// execText is the text sent for a statement (toks are its code tokens):
// without a trailing "/" line, and without trailing semicolons unless it is
// PL/SQL.
func execText(text string, toks []token, unit bool) string {
	end, n := len(text), len(toks)
	if toks[n-1].kind == tokSlash {
		end, n = toks[n-1].start, n-1
	}
	for !unit && n > 0 && toks[n-1].kind == tokSemi {
		end, n = toks[n-1].start, n-1
	}
	return strings.TrimRight(text[:end], " \t\r\n\f\v")
}

func isQuery(toks []token) bool {
	for _, t := range toks {
		if t.kind != tokOpen {
			return t.kind == tokWord && (t.low == "select" || t.low == "with")
		}
	}
	return false
}

func isDML(w []string) bool {
	if len(w) == 0 {
		return false
	}
	switch w[0] {
	case "insert", "update", "delete", "merge":
		return true
	}
	return false
}

// run sends text and wraps the outcome; toks are the statement's code
// tokens.
func (s *session) run(ctx context.Context, text string, toks []token, unit bool) (driver.Result, error) {
	query := isQuery(toks)
	words := codeWords(toks, 12)
	var r *result
	err := s.call(ctx, text, func() error {
		if query {
			rows, err := s.link.oc.QueryContext(ctx, text, nil)
			if err != nil {
				return err
			}
			r = newRows(s, text, rows)
			return nil
		}
		res, err := s.link.oc.ExecContext(ctx, text, nil)
		if err != nil {
			return err
		}
		r = &result{s: s, affected: -1, done: true, closed: true}
		if isDML(words) {
			r.affected, _ = res.RowsAffected()
		}
		return nil
	})
	if err != nil {
		err = s.failure(err, text)
		s.poll(true)
		return nil, err
	}
	// With a cursor open (SELECT ... FOR UPDATE) the commit waits for the
	// last row.
	s.poll(!query)
	if unit {
		if err := s.compileErrors(ctx, text, toks); err != nil {
			return nil, err
		}
	}
	if query {
		s.cur = r
	} else {
		r.tag = tagFor(words, r.affected)
	}
	return r, nil
}

// call runs fn, a round trip on the session's connection, and cancels it
// server-side if ctx ends meanwhile.
func (s *session) call(ctx context.Context, text string, fn func() error) error {
	if !s.link.alive() {
		return errLost
	}
	s.aborted.Store(false)
	stop := s.guard(ctx, text)
	err := fn()
	stop()
	return err
}

// guard watches ctx while a call is in progress; the returned function
// ends the watch and waits for a cancellation already under way.
func (s *session) guard(ctx context.Context, text string) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(finished)
		s.interrupt(done, text)
	})
	return func() {
		close(done)
		if !stop() {
			<-finished
		}
	}
}

// interrupt stops a call whose context ended. go-ora sends an in-band break
// for Execute, which stops CPU-bound work but not waits such as
// DBMS_SESSION.SLEEP or locks, and nothing while rows are fetched. When the
// user may, ALTER SYSTEM CANCEL SQL is sent from another session, targeted at
// the statement's SQL_ID so that it cannot hit a later statement; if the
// statement is running recursive SQL the cancel is untargeted and its
// possible leftover is absorbed by the next poll. A call still running
// after cancelDeadline has its socket closed: the session is lost.
func (s *session) interrupt(done <-chan struct{}, text string) {
	if !wait(done, cancelGrace) {
		return
	}
	if s.ident != "" {
		err := s.parent.cancelSQL(s.ident, sqlID(text))
		if oraCode(err) == 866 && wait(done, cancelGrace) {
			s.stray.Store(true)
			s.parent.cancelSQL(s.ident, "")
		}
	}
	if wait(done, cancelDeadline) {
		s.aborted.Store(true)
		s.link.sock.abort()
	}
}

// wait reports whether d elapsed before done was closed.
func wait(done <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return false
	case <-t.C:
		return true
	}
}

// sqlID computes Oracle's SQL_ID of a statement text: the low 64 bits of the
// MD5 of the text and a NUL byte, in base 32.
func sqlID(text string) string {
	h := md5.Sum([]byte(text + "\x00"))
	n := uint64(binary.LittleEndian.Uint32(h[8:12]))<<32 | uint64(binary.LittleEndian.Uint32(h[12:16]))
	const alphabet = "0123456789abcdfghjkmnpqrstuvwxyz"
	var b [13]byte
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = alphabet[n&31]
		n >>= 5
	}
	return string(b[:])
}

// cancelSQL sends ALTER SYSTEM CANCEL SQL for a session (and SQL_ID) from
// the control session, opened on first use.
func (c *conn) cancelSQL(ident, id string) error {
	c.ctlMu.Lock()
	defer c.ctlMu.Unlock()
	if c.closed.Load() {
		return errClosed
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelDeadline)
	defer cancel()
	if c.ctl == nil || !c.ctl.alive() {
		l, err := c.st.dial(ctx)
		if err != nil {
			return err
		}
		c.ctl = l
	}
	arg := ident
	if id != "" {
		arg += ", " + id
	}
	_, err := c.ctl.exec(ctx, "ALTER SYSTEM CANCEL SQL '"+arg+"'")
	c.ctl.check(err)
	return err
}

// failure converts the error of a user call.
func (s *session) failure(err error, text string) error {
	if s.aborted.Load() {
		return &friendlyError{msg: "the server did not stop the statement after the cancel request; the session was disconnected", err: err}
	}
	s.link.check(err)
	return queryError(err, text)
}

// pollBlock fetches DBMS_OUTPUT lines and the transaction state, committing
// first when :2 is 1 (autocommit).
const pollBlock = `DECLARE
  l DBMS_OUTPUT.CHARARR;
  n INTEGER := :1;
  r CLOB;
BEGIN
  DBMS_OUTPUT.GET_LINES(l, n);
  FOR i IN 1 .. n LOOP
    r := r || l(i) || CHR(10);
  END LOOP;
  IF :2 = 1 AND DBMS_TRANSACTION.LOCAL_TRANSACTION_ID IS NOT NULL THEN
    COMMIT;
  END IF;
  :3 := r;
  :4 := n;
  :5 := DBMS_TRANSACTION.LOCAL_TRANSACTION_ID;
END;`

// poll runs after every statement: it collects DBMS_OUTPUT as messages,
// commits in autocommit mode (unless commit is false) and records whether a
// transaction is open. Failures are ignored; a dead connection is noticed by
// the next call.
func (s *session) poll(commit bool) {
	if !s.link.alive() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelDeadline)
	defer cancel()
	ac := int64(0)
	if commit && s.autoCommit {
		ac = 1
	}
	total := 0
	for {
		var out go_ora.Clob
		var n int64
		var tx string
		args := []any{int64(outputBatch), ac, sql.Out{Dest: &out}, sql.Out{Dest: &n}, go_ora.Out{Dest: &tx, Size: 64}}
		_, err := s.link.oc.ExecContext(ctx, pollBlock, named(args))
		if oraCode(err) == 1013 && s.stray.Swap(false) {
			continue
		}
		if err != nil {
			s.link.check(err)
			return
		}
		s.stray.Store(false)
		s.inTx.Store(tx != "")
		if n > 0 {
			for line := range strings.SplitSeq(strings.TrimSuffix(out.String, "\n"), "\n") {
				s.addMsg("OUTPUT", line)
			}
		}
		total += int(n)
		if n < outputBatch {
			return
		}
		if total >= maxOutputLines {
			s.link.exec(ctx, "BEGIN DBMS_OUTPUT.DISABLE; DBMS_OUTPUT.ENABLE(NULL); END;")
			s.addMsg("INFO", fmt.Sprintf("DBMS_OUTPUT truncated after %d lines", total))
			return
		}
	}
}

func (s *session) closeCur() {
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
}

// InTx reports whether the session has uncommitted changes (or locks), as
// seen by DBMS_TRANSACTION after the last statement.
func (s *session) InTx() bool { return s.inTx.Load() }

// SetAutoCommit switches modes; turning autocommit back on commits an open
// transaction, as JDBC does.
func (s *session) SetAutoCommit(ctx context.Context, on bool) error {
	if on && !s.autoCommit {
		if err := s.Commit(ctx); err != nil {
			return err
		}
	}
	s.autoCommit = on
	return nil
}

func (s *session) Commit(ctx context.Context) error   { return s.end(ctx, "COMMIT") }
func (s *session) Rollback(ctx context.Context) error { return s.end(ctx, "ROLLBACK") }

func (s *session) end(ctx context.Context, verb string) error {
	s.closeCur()
	if !s.InTx() {
		return nil
	}
	if !s.link.alive() {
		return errLost
	}
	_, err := s.link.exec(ctx, verb)
	if err != nil {
		s.link.check(err)
		return queryError(err, "")
	}
	s.inTx.Store(false)
	return nil
}

// SearchPathSource is the optional interface sessions implement to tell
// autocomplete which schemas resolve unqualified names, in resolution order.
type SearchPathSource interface {
	SearchPath(ctx context.Context) ([]string, error)
}

var _ SearchPathSource = (*session)(nil)

// SearchPath returns the session's CURRENT_SCHEMA, reflecting any ALTER
// SESSION the user ran. Oracle allows it while a cursor is open.
func (s *session) SearchPath(ctx context.Context) ([]string, error) {
	if !s.link.alive() {
		return nil, errLost
	}
	var schema string
	err := s.link.queryRow(ctx, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual", nil, &schema)
	if err != nil {
		s.link.check(err)
		return nil, queryError(err, "")
	}
	return []string{schema}, nil
}

// Close rolls back an open transaction (Oracle would commit it on a normal
// logoff) and ends the session.
func (s *session) Close() error {
	s.closeCur()
	if s.link.alive() && s.InTx() {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		s.link.exec(ctx, "ROLLBACK")
		cancel()
	}
	return s.link.close()
}

// tagFor names a completed non-query statement: "INSERT 3", "CREATE TABLE",
// "PL/SQL BLOCK", "COMMIT".
func tagFor(w []string, affected int64) string {
	if len(w) == 0 {
		return ""
	}
	verb := strings.ToUpper(w[0])
	switch w[0] {
	case "insert", "update", "delete", "merge":
		return fmt.Sprintf("%s %d", verb, max(affected, 0))
	case "declare", "begin":
		return "PL/SQL BLOCK"
	case "create", "alter", "drop":
		for i := 1; i < len(w); i++ {
			if objectModifiers[w[i]] {
				continue
			}
			kind := strings.ToUpper(w[i])
			if i+1 < len(w) && kindSuffixes[w[i+1]] {
				kind += " " + strings.ToUpper(w[i+1])
			}
			return verb + " " + kind
		}
	}
	return verb
}

var (
	objectModifiers = map[string]bool{"or": true, "replace": true, "editionable": true, "noneditionable": true,
		"editioning": true, "global": true, "temporary": true, "private": true, "public": true, "unique": true,
		"bitmap": true, "force": true, "noforce": true, "multivalue": true, "shared": true, "and": true,
		"compile": true, "resolve": true, "immutable": true, "blockchain": true, "sharded": true, "duplicated": true}
	kindSuffixes = map[string]bool{"body": true, "view": true, "link": true, "log": true, "source": true, "class": true}
)

// oraCode returns the ORA error number of err, or 0.
func oraCode(err error) int {
	if err == nil {
		return 0
	}
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		var n int
		fmt.Sscanf(strings.TrimPrefix(qe.Code, "ORA-"), "%d", &n)
		return n
	}
	if oe := asOracleError(err); oe != nil {
		return oe.ErrCode
	}
	return 0
}
