package mongo

import (
	"context"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/gxd3v/quarry/internal/driver"
)

// maxPrefetch bounds how many documents Execute reads up front to learn
// the result's columns. Only documents already received in the cursor's
// first batch are taken, so no extra round trip is made.
const maxPrefetch = 1000

// docResult streams documents. Its columns are the union of the top-level
// keys of the prefetched documents (_id first, then in first-seen order),
// plus {document}, the whole document as relaxed Extended JSON. Keys first
// seen in later documents only show up in {document}.
type docResult struct {
	s        *session
	cur      *mongo.Cursor // nil when every document is in docs
	docs     []bson.Raw    // prefetched, not yet returned
	keys     []string
	index    map[string]int
	cols     []driver.Column
	err      error
	done     bool
	closed   bool
	affected int64
	tag      string
}

var _ driver.Result = (*docResult)(nil)

// newCursorResult reads the cursor's first batch to learn the columns.
// Errors raised by the first batch are returned here, not from Next.
func newCursorResult(ctx context.Context, s *session, cur *mongo.Cursor) (*docResult, error) {
	r := &docResult{s: s, cur: cur, affected: -1}
	if cur.Next(ctx) {
		r.docs = append(r.docs, slices.Clone(cur.Current))
		for len(r.docs) < maxPrefetch && cur.RemainingBatchLength() > 0 && cur.Next(ctx) {
			r.docs = append(r.docs, slices.Clone(cur.Current))
		}
	}
	if err := cur.Err(); err != nil {
		cur.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	if len(r.docs) == 0 || cur.ID() == 0 && cur.RemainingBatchLength() == 0 {
		r.finishCursor(ctx)
	}
	r.describe()
	return r, nil
}

// newDocsResult wraps documents already in memory.
func newDocsResult(docs ...bson.Raw) *docResult {
	r := &docResult{docs: docs, done: true, affected: -1}
	r.describe()
	return r
}

func (r *docResult) finishCursor(ctx context.Context) {
	if r.cur != nil {
		r.cur.Close(context.WithoutCancel(ctx))
		r.cur = nil
	}
	r.done = true
}

func (r *docResult) describe() {
	r.index = map[string]int{}
	types := map[string]string{}
	add := func(k string) {
		if _, ok := r.index[k]; !ok {
			r.index[k] = len(r.keys)
			r.keys = append(r.keys, k)
		}
	}
	for _, d := range r.docs {
		if _, err := d.LookupErr("_id"); err == nil {
			add("_id")
			break
		}
	}
	for _, d := range r.docs {
		els, _ := d.Elements()
		for _, e := range els {
			k := e.Key()
			add(k)
			t := typeName(e.Value())
			switch prev, seen := types[k]; {
			case t == "null" || t == "undefined":
				if !seen {
					types[k] = ""
				}
			case !seen || prev == "":
				types[k] = t
			case prev != t:
				types[k] = "mixed"
			}
		}
	}
	for _, k := range r.keys {
		r.cols = append(r.cols, driver.Column{Name: k, Type: types[k], Nullable: true})
	}
	r.cols = append(r.cols, driver.Column{Name: docColumn, Type: "document"})
}

func (r *docResult) Columns() []driver.Column { return r.cols }
func (r *docResult) Affected() int64          { return r.affected }

// Tag summarises results without rows (e.g. "switched to db app").
func (r *docResult) Tag() string { return r.tag }

func (r *docResult) row(d bson.Raw) []any {
	row := make([]any, len(r.keys)+1)
	if els, err := d.Elements(); err == nil {
		for _, e := range els {
			if i, ok := r.index[e.Key()]; ok {
				row[i] = normalize(e.Value())
			}
		}
	}
	row[len(r.keys)] = driver.JSON(extJSON(d))
	return row
}

func (r *docResult) Next(ctx context.Context, n int) ([][]any, error) {
	n = max(n, 1)
	var out [][]any
	for len(out) < n && len(r.docs) > 0 {
		out = append(out, r.row(r.docs[0]))
		r.docs[0] = nil
		r.docs = r.docs[1:]
	}
	if !r.done && !r.closed && r.cur != nil {
		for len(out) < n {
			if !r.cur.Next(ctx) {
				if err := r.cur.Err(); err != nil {
					r.err = queryError(err)
				}
				r.finishCursor(ctx)
				break
			}
			out = append(out, r.row(r.cur.Current))
		}
	}
	if r.err != nil && len(out) == 0 {
		return nil, r.err
	}
	return out, nil
}

// Close discards the rest of the result; an open server cursor is killed.
func (r *docResult) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.docs = nil
	if r.cur != nil {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		r.cur.Close(ctx)
		cancel()
		r.cur = nil
	}
	r.done = true
	if r.s != nil && r.s.cur == r {
		r.s.cur = nil
	}
	return nil
}

// rowsResult is a small in-memory table: write outcomes, counts, distinct
// values, names.
type rowsResult struct {
	cols     []driver.Column
	rows     [][]any
	affected int64
	tag      string
}

var _ driver.Result = (*rowsResult)(nil)

func (r *rowsResult) Columns() []driver.Column { return r.cols }
func (r *rowsResult) Affected() int64          { return r.affected }
func (r *rowsResult) Tag() string              { return r.tag }
func (r *rowsResult) Close() error             { r.rows = nil; return nil }

func (r *rowsResult) Next(_ context.Context, n int) ([][]any, error) {
	n = max(n, 1)
	k := min(n, len(r.rows))
	out := r.rows[:k:k]
	r.rows = r.rows[k:]
	return out, nil
}

// singleRow builds a one-row result from name/value pairs.
func singleRow(affected int64, pairs ...any) *rowsResult {
	r := &rowsResult{affected: affected}
	row := make([]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		name := pairs[i].(string)
		v := pairs[i+1]
		r.cols = append(r.cols, driver.Column{Name: name, Type: goTypeName(v), Nullable: true})
		row = append(row, v)
	}
	r.rows = [][]any{row}
	return r
}

// column builds a one-column result.
func column(name string, values []any) *rowsResult {
	r := &rowsResult{affected: -1, cols: []driver.Column{{Name: name, Nullable: true}}}
	typ := ""
	for _, v := range values {
		r.rows = append(r.rows, []any{v})
		switch t := goTypeName(v); {
		case t == "null":
		case typ == "":
			typ = t
		case typ != t:
			typ = "mixed"
		}
	}
	r.cols[0].Type = typ
	return r
}

// tagResult is a result without rows, e.g. for `use`.
func tagResult(tag string) *rowsResult {
	return &rowsResult{affected: -1, tag: tag}
}

func goTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int64:
		return "long"
	case float64:
		return "double"
	case string:
		return "string"
	case driver.JSON:
		return "object"
	case driver.Number:
		return "decimal"
	case []byte:
		return "binData"
	case time.Time:
		return "date"
	}
	return ""
}
