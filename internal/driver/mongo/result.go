package mongo

import (
	"context"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/gxd3v/quarry/internal/driver"
)

// DocumentColumn is the last column of every document result: the whole
// document as relaxed Extended JSON (driver.JSON). The UI shows it in the
// document view and may send it back to Apply to replace a document.
const DocumentColumn = "{document}"

// batchSize is the cursor batch size of finds and aggregations, unless the
// statement sets one.
const batchSize = 1000

// docResult streams documents from a server cursor (or from memory). Its
// columns are the union of the top-level fields of the cursor's first batch
// (_id first, then in order of first appearance), typed with their $type
// alias ("mixed" when they differ), followed by DocumentColumn. Fields first
// seen after the first batch have no column of their own; they are only in
// DocumentColumn.
type docResult struct {
	s        *session
	cur      *mongo.Cursor // nil once exhausted or closed
	docs     []bson.Raw    // prefetched, not yet returned
	keys     []string
	index    map[string]int
	cols     []driver.Column
	affected int64
}

var _ driver.Result = (*docResult)(nil)

// newCursorResult reads the cursor's first batch to learn the columns, so
// errors and slow queries surface from Execute rather than from Next.
func newCursorResult(ctx context.Context, s *session, cur *mongo.Cursor) (*docResult, error) {
	r := &docResult{s: s, cur: cur, affected: -1}
	for cur.Next(ctx) {
		r.docs = append(r.docs, slices.Clone(cur.Current))
		if cur.RemainingBatchLength() == 0 {
			break
		}
	}
	if err := cur.Err(); err != nil {
		r.closeCursor()
		return nil, err
	}
	if cur.ID() == 0 && cur.RemainingBatchLength() == 0 {
		r.closeCursor()
	} else {
		cur.SetBatchSize(batchSize)
	}
	r.describe()
	return r, nil
}

// newDocsResult wraps documents already in memory.
func newDocsResult(docs ...bson.Raw) *docResult {
	r := &docResult{docs: docs, affected: -1}
	r.describe()
	return r
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
			add(e.Key())
			types[e.Key()] = mergeType(types[e.Key()], typeName(e.Value()))
		}
	}
	r.cols = make([]driver.Column, 0, len(r.keys)+1)
	for _, k := range r.keys {
		r.cols = append(r.cols, driver.Column{Name: k, Type: types[k], Nullable: true})
	}
	r.cols = append(r.cols, driver.Column{Name: DocumentColumn, Type: "document"})
}

// mergeType folds a value's type into a column's; null and undefined do
// not decide a type.
func mergeType(prev, t string) string {
	switch {
	case t == "null" || t == "undefined":
		return prev
	case prev == "" || prev == t:
		return t
	}
	return "mixed"
}

func (r *docResult) Columns() []driver.Column { return r.cols }
func (r *docResult) Affected() int64          { return r.affected }

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
	if len(out) == n || r.cur == nil {
		return out, nil
	}
	err := r.s.interruptible(ctx, func(ctx context.Context) error {
		for len(out) < n && r.cur.Next(ctx) {
			out = append(out, r.row(r.cur.Current))
		}
		return r.cur.Err()
	})
	if err != nil || len(out) < n {
		r.closeCursor()
	}
	if err != nil {
		r.s.afterError(ctx, err)
		if len(out) == 0 {
			return nil, queryError(err)
		}
	}
	return out, nil
}

func (r *docResult) closeCursor() {
	if r.cur == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	r.cur.Close(ctx)
	cancel()
	r.cur = nil
}

// Close discards the rest of the result; an open server cursor is killed.
func (r *docResult) Close() error {
	r.docs = nil
	r.closeCursor()
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
func (r *rowsResult) Close() error             { r.rows = nil; return nil }

// Tag summarises results without rows (e.g. "switched to db app").
func (r *rowsResult) Tag() string { return r.tag }

func (r *rowsResult) Next(_ context.Context, n int) ([][]any, error) {
	k := min(max(n, 1), len(r.rows))
	out := r.rows[:k:k]
	r.rows = r.rows[k:]
	return out, nil
}

// singleRow builds a one-row result from name/value pairs.
func singleRow(affected int64, pairs ...any) *rowsResult {
	r := &rowsResult{affected: affected}
	row := make([]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		v := pairs[i+1]
		r.cols = append(r.cols, driver.Column{Name: pairs[i].(string), Type: goTypeName(v), Nullable: true})
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
		typ = mergeType(typ, goTypeName(v))
	}
	r.cols[0].Type = typ
	return r
}

// table builds a result with fixed columns.
func table(names []string, rows [][]any) *rowsResult {
	r := &rowsResult{affected: -1, rows: rows}
	for i, n := range names {
		typ := ""
		for _, row := range rows {
			typ = mergeType(typ, goTypeName(row[i]))
		}
		r.cols = append(r.cols, driver.Column{Name: n, Type: typ, Nullable: true})
	}
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
