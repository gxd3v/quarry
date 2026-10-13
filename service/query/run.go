package query

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/workspace"
	"github.com/gxd3v/quarry/service"
)

// cursor reads a driver result and can look one row ahead to tell whether
// more rows remain.
type cursor struct {
	res driver.Result
	buf [][]any
	eof bool
}

func (c *cursor) next(ctx context.Context, n int) ([][]any, error) {
	out := c.buf
	c.buf = nil
	if len(out) >= n || c.eof {
		return out, nil
	}
	rows, err := c.res.Next(ctx, n-len(out))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		c.eof = true
	}
	if out == nil {
		return rows, nil
	}
	return append(out, rows...), nil
}

func (c *cursor) more(ctx context.Context) (bool, error) {
	if len(c.buf) > 0 {
		return true, nil
	}
	if c.eof {
		return false, nil
	}
	rows, err := c.res.Next(ctx, 1)
	if err != nil {
		return false, err
	}
	c.buf, c.eof = rows, len(rows) == 0
	return !c.eof, nil
}

func (c *cursor) close() {
	if err := c.res.Close(); err != nil {
		slog.Debug("query: close result", "err", err)
	}
}

// emitter sends events until the first failure, after which the stream is
// considered gone.
type emitter struct {
	send func(*quarryv1.RunEvent) error
	err  error
}

func (e *emitter) emit(ev *quarryv1.RunEvent) bool {
	if e.err == nil {
		e.err = e.send(ev)
	}
	return e.err == nil
}

func (e *emitter) messages(sess driver.Session) bool {
	ms, ok := sess.(driver.MessageSource)
	if !ok {
		return e.err == nil
	}
	for _, m := range ms.DrainMessages() {
		e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Message{Message: &quarryv1.ServerMessage{Severity: m.Severity, Text: m.Text}}})
	}
	return e.err == nil
}

// pump streams up to limit rows (all when negative), starting with a batch of
// first rows, and reports how many it sent and whether more remain.
func (e *emitter) pump(ctx context.Context, c *cursor, limit int64, first int) (int64, bool, error) {
	var sent int64
	size := first
	for limit < 0 || sent < limit {
		n := size
		if limit >= 0 {
			n = int(min(int64(n), limit-sent))
		}
		rows, err := c.next(ctx, n)
		if err != nil {
			return sent, false, err
		}
		if len(rows) == 0 {
			return sent, false, nil
		}
		if !e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Rows{Rows: &quarryv1.RowBatch{Rows: service.RowsOf(rows)}}}) {
			return sent, false, e.err
		}
		sent += int64(len(rows))
		size = service.Batch
	}
	more, err := c.more(ctx)
	return sent, more, err
}

func queryError(script string, st driver.Statement, err error) *quarryv1.QueryError {
	qe, ok := errors.AsType[*driver.QueryError](err)
	if !ok {
		return &quarryv1.QueryError{Message: err.Error(), Position: -1}
	}
	pos := int32(-1)
	if qe.Position >= 0 {
		pos = int32(service.UTF16Offset(script, st.Offset+qe.Position))
	}
	return &quarryv1.QueryError{Message: qe.Msg, Code: qe.Code, Detail: qe.Detail, Hint: qe.Hint, Position: pos}
}

func tagOf(r driver.Result) string {
	if t, ok := r.(interface{ Tag() string }); ok {
		return t.Tag()
	}
	return ""
}

func (s *Service) Run(ctx context.Context, req *connect.Request[quarryv1.RunRequest], stream *connect.ServerStream[quarryv1.RunEvent]) error {
	return s.run(ctx, req.Msg, stream.Send)
}

func (s *Service) Fetch(ctx context.Context, req *connect.Request[quarryv1.FetchRequest], stream *connect.ServerStream[quarryv1.RunEvent]) error {
	return s.fetch(ctx, req.Msg, stream.Send)
}

func (s *Service) limit(n int32) int64 {
	switch {
	case n == 0:
		return int64(s.maxRows)
	case n < 0:
		return -1
	}
	return int64(n)
}

func statements(sp splitter, req *quarryv1.RunRequest) []driver.Statement {
	script := req.Script
	all := sp.split(script)
	start := service.ByteOffset(script, int(req.Start))
	if req.Mode == quarryv1.RunMode_RUN_MODE_RANGE {
		return inRange(all, start, service.ByteOffset(script, int(req.End)))
	}
	if st, ok := sp.at(all, script, start); ok {
		return []driver.Statement{st}
	}
	return nil
}

func (s *Service) run(ctx context.Context, req *quarryv1.RunRequest, send func(*quarryv1.RunEvent) error) error {
	t, err := s.tab(req.TabId)
	if err != nil {
		return err
	}
	ctx, release, err := t.begin(ctx)
	if err != nil {
		return err
	}
	defer release()
	t.discard()
	sess, cfg, err := s.session(ctx, t)
	if err != nil {
		return err
	}
	_, scope := t.target()

	began := time.Now()
	script := req.Script
	stmts := statements(splitterFor(cfg.Kind), req)
	limit := s.limit(req.MaxRows)
	e := &emitter{send: send}
	e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Started{Started: &quarryv1.RunStarted{
		RunId: uuid.New().String(), Statements: int32(len(stmts)),
	}}})

	conv := utf16Conv{s: script}
	canceled := false
	for i, st := range stmts {
		if ctx.Err() != nil || e.err != nil {
			canceled = true
			break
		}
		e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Statement{Statement: &quarryv1.StatementStarted{
			Index: int32(i), Range: rangeOf(&conv, st),
		}}})
		last := i == len(stmts)-1
		h := workspace.Entry{At: time.Now(), Conn: cfg.ID, Scope: scope, Text: st.Text, Rows: -1}
		var (
			c    *cursor
			rows int64
			more bool
		)
		t0 := time.Now()
		res, err := sess.Execute(ctx, st)
		if err == nil {
			c = &cursor{res: res}
			if cols := res.Columns(); len(cols) > 0 {
				e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Columns{Columns: &quarryv1.ResultColumns{Columns: service.ColumnsOf(cols)}}})
				rows, more, err = e.pump(ctx, c, limit, service.FirstBatch)
				h.Rows = rows
			} else {
				h.Rows = res.Affected()
			}
		}
		h.Duration = time.Since(t0)
		if err != nil || ctx.Err() != nil || e.err != nil {
			if c != nil {
				c.close()
			}
			switch {
			case ctx.Err() != nil || e.err != nil:
				canceled = true
				h.Err = "canceled"
			default:
				qe := queryError(script, st, err)
				h.Err = qe.Message
				e.messages(sess)
				e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Error{Error: qe}})
			}
			s.record(h)
			break
		}
		s.record(h)
		e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Done{Done: &quarryv1.StatementDone{
			Affected: res.Affected(), Tag: tagOf(res), ElapsedMs: h.Duration.Milliseconds(), Rows: rows, HasMore: more,
		}}})
		e.messages(sess)
		if last && more {
			t.open = &openResult{cur: c, rows: rows, script: script, stmt: st}
		} else {
			c.close()
		}
	}
	return s.finish(e, t, began, canceled)
}

func (s *Service) finish(e *emitter, t *tab, began time.Time, canceled bool) error {
	e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Finished{Finished: &quarryv1.RunFinished{
		ElapsedMs: time.Since(began).Milliseconds(), Tx: t.txState(), Canceled: canceled,
	}}})
	if canceled {
		return nil
	}
	return e.err
}

func (s *Service) record(h workspace.Entry) {
	if s.history == nil {
		return
	}
	if err := s.history.AppendHistory(h); err != nil {
		slog.Warn("query: append history", "err", err)
	}
}

func (s *Service) fetch(ctx context.Context, req *quarryv1.FetchRequest, send func(*quarryv1.RunEvent) error) error {
	t, err := s.tab(req.TabId)
	if err != nil {
		return err
	}
	ctx, release, err := t.begin(ctx)
	if err != nil {
		return err
	}
	defer release()
	o := t.open
	if o == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no open result to fetch from"))
	}

	began := time.Now()
	e := &emitter{send: send}
	rows, more, err := e.pump(ctx, o.cur, s.limit(req.MaxRows), service.Batch)
	o.rows += rows
	canceled := ctx.Err() != nil || e.err != nil
	switch {
	case canceled:
		t.discard()
	case err != nil:
		t.discard()
		e.messages(t.sess)
		e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Error{Error: queryError(o.script, o.stmt, err)}})
	default:
		res := o.cur.res
		e.emit(&quarryv1.RunEvent{Event: &quarryv1.RunEvent_Done{Done: &quarryv1.StatementDone{
			Affected: res.Affected(), Tag: tagOf(res), ElapsedMs: time.Since(began).Milliseconds(), Rows: o.rows, HasMore: more,
		}}})
		e.messages(t.sess)
		if !more {
			t.discard()
		}
	}
	return s.finish(e, t, began, canceled)
}
