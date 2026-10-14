package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gxd3v/quarry/internal/driver"
)

var errCopy = errors.New("COPY FROM STDIN and TO STDOUT are not supported in scripts; they run in the terminal")

// session is one editor tab's dedicated connection. Like pgx.Conn it is not
// safe for concurrent use, except DrainMessages and InTx which may be called
// from any goroutine.
type session struct {
	pc         *pgx.Conn
	parent     *conn
	db         string
	autoCommit bool
	inTx       atomic.Bool
	cur        *result

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
	s := &session{parent: c, autoCommit: true}
	var rt map[string]string
	if scope.Schema != "" {
		path := pgx.Identifier{scope.Schema}.Sanitize()
		if scope.Schema != "public" {
			path += ", public"
		}
		rt = map[string]string{"search_path": path}
	}
	pc, err := c.connect(ctx, scope.Database, s.onNotice, rt)
	if err != nil {
		return nil, err
	}
	if c.readOnly {
		if _, err := pc.Exec(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"); err != nil {
			cctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			pc.Close(cctx)
			cancel()
			return nil, queryError(err, "")
		}
	}
	s.pc = pc
	s.db = pc.Config().Database
	return s, nil
}

func (s *session) onNotice(_ *pgconn.PgConn, n *pgconn.Notice) {
	sev := n.SeverityUnlocalized
	if sev == "" {
		sev = n.Severity
	}
	s.msgMu.Lock()
	s.msgs = append(s.msgs, driver.Message{Severity: sev, Text: n.Message})
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

func (s *session) Execute(ctx context.Context, stmt driver.Statement) (driver.Result, error) {
	s.closeCur()
	words := keywords(stmt.Text)
	if len(words) == 0 {
		return &result{done: true, closed: true, affected: -1}, nil
	}
	if words[0] == "copy" && (slices.Contains(words, "stdin") || slices.Contains(words, "stdout")) {
		return nil, errCopy
	}
	if !s.autoCommit && s.pc.PgConn().TxStatus() == 'I' && needsTx(words) {
		if _, err := s.pc.Exec(ctx, "BEGIN"); err != nil {
			s.syncTx()
			return nil, queryError(err, "")
		}
	}

	// The query runs on its own context so that cancelling ctx only matters
	// while Execute (or a later Next) is actually waiting on the server.
	qctx, qcancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, qcancel)
	defer stop()

	rows, err := s.pc.Query(qctx, stmt.Text, pgx.QueryExecModeExec)
	if err != nil {
		if rows != nil {
			rows.Close()
		}
		qcancel()
		s.syncTx()
		return nil, queryError(err, stmt.Text)
	}
	r := &result{s: s, text: stmt.Text, rows: rows, cancel: qcancel, affected: -1, txAtStart: s.pc.PgConn().TxStatus() != 'I'}
	fields := rows.FieldDescriptions()
	if len(fields) == 0 {
		if err := r.finish(); err != nil {
			return nil, queryError(err, stmt.Text)
		}
		r.closed = true
		r.affected = affected(r.tag)
		return r, nil
	}
	r.describe(ctx, fields)
	// Fetch the first row now so errors raised by the first row (and slow
	// queries) surface from Execute rather than from the first Next.
	if rows.Next() {
		r.pending = r.decode(rows.RawValues())
	} else if err := r.finish(); err != nil {
		return nil, queryError(err, stmt.Text)
	}
	s.cur = r
	return r, nil
}

// needsTx reports whether manual-commit mode should open a transaction
// before the statement; transaction control and commands that refuse to run
// inside a transaction block are sent as is.
func needsTx(w []string) bool {
	switch w[0] {
	case "begin", "start", "commit", "end", "rollback", "abort", "savepoint", "release",
		"prepare", "vacuum", "checkpoint", "discard":
		return false
	}
	if len(w) >= 2 {
		switch w[0] + " " + w[1] {
		case "create database", "drop database", "alter system", "create tablespace", "drop tablespace":
			return false
		}
	}
	switch w[0] {
	case "create", "drop", "reindex":
		if slices.Contains(w[:min(5, len(w))], "concurrently") {
			return false
		}
	}
	return true
}

// affected returns the row count of commands that report one (DML,
// CREATE TABLE AS, ...) and -1 for the rest (DDL, SET, ...).
func affected(tag pgconn.CommandTag) int64 {
	for _, p := range []string{"INSERT ", "UPDATE ", "DELETE ", "MERGE ", "SELECT ", "COPY ", "MOVE ", "FETCH "} {
		if strings.HasPrefix(tag.String(), p) {
			return tag.RowsAffected()
		}
	}
	return -1
}

func (s *session) closeCur() {
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
}

func (s *session) syncTx() {
	st := s.pc.PgConn().TxStatus()
	s.inTx.Store(st == 'T' || st == 'E')
}

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

func (s *session) Commit(ctx context.Context) error {
	s.closeCur()
	s.syncTx()
	if !s.InTx() {
		return nil
	}
	tag, err := s.pc.Exec(ctx, "COMMIT")
	s.syncTx()
	if err != nil {
		return queryError(err, "")
	}
	if tag.String() == "ROLLBACK" {
		return errors.New("transaction was aborted by an earlier error and has been rolled back")
	}
	return nil
}

func (s *session) Rollback(ctx context.Context) error {
	s.closeCur()
	s.syncTx()
	if !s.InTx() {
		return nil
	}
	_, err := s.pc.Exec(ctx, "ROLLBACK")
	s.syncTx()
	if err != nil {
		return queryError(err, "")
	}
	return nil
}

// SearchPathSource is the optional interface sessions implement to tell
// autocomplete which schemas resolve unqualified names, in resolution order.
// The UI discovers it with a type assertion on driver.Session.
type SearchPathSource interface {
	SearchPath(ctx context.Context) ([]string, error)
}

var (
	_ SearchPathSource = (*session)(nil)

	errBusy = errors.New("session is busy: a result is still open")
)

// SearchPath returns current_schemas(false): the existing schemas of the
// session's search_path, in order, without the implicit pg_catalog and
// pg_temp. It reflects any SET search_path the user ran in this tab.
//
// Like Execute it uses the session's connection, so it must only be called
// while the session is idle (no Execute running, from the goroutine that
// drives the session); it returns errBusy rather than discarding a result
// the user is still paging through. It never opens or aborts a transaction.
func (s *session) SearchPath(ctx context.Context) ([]string, error) {
	if s.cur != nil && !s.cur.done {
		return nil, errBusy
	}
	if s.pc.PgConn().TxStatus() != 'I' {
		// A cancel request would abort the user's transaction; the query is
		// instantaneous, so let it finish.
		ctx = context.WithoutCancel(ctx)
	}
	var path []string
	if err := s.pc.QueryRow(ctx, "SELECT current_schemas(false)::text[]").Scan(&path); err != nil {
		s.syncTx()
		return nil, queryError(err, "")
	}
	return path, nil
}

func (s *session) Close() error {
	s.closeCur()
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	return s.pc.Close(ctx)
}

// queryError converts server errors to *driver.QueryError with Position as
// a byte offset into text; other errors are wrapped.
func queryError(err error, text string) error {
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		return qe
	}
	if fe, ok := errors.AsType[*friendlyError](err); ok {
		return fe
	}
	pe, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		if errors.Is(err, errClosed) {
			return err
		}
		return fmt.Errorf("postgres: %w", err)
	}
	return &driver.QueryError{
		Msg:      pe.Message,
		Detail:   pe.Detail,
		Hint:     pe.Hint,
		Code:     pe.Code,
		Position: bytePos(text, int(pe.Position)),
	}
}

// bytePos converts PostgreSQL's 1-based character position into a 0-based
// byte offset into text, or -1 when there is none.
func bytePos(text string, pos int) int {
	if pos <= 0 || text == "" {
		return -1
	}
	off := 0
	for i := 1; i < pos && off < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[off:])
		off += size
	}
	return off
}
