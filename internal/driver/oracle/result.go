package oracle

import (
	"bytes"
	"context"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

// valueKind tells how a column's values are normalized.
type valueKind int

const (
	kindText    valueKind = iota
	kindInt               // NUMBER(p) / NUMBER(p,0): int64 when it fits
	kindNumber            // unconstrained NUMBER, FLOAT: int64 when integral and fits
	kindDecimal           // NUMBER(p,s>0): always driver.Number
	kindJSON
)

// columnType names a result column the way Oracle does, from go-ora's wire
// type. go-ora fetches LOBs inline, so CLOB and BLOB arrive as LONG types,
// while JSON stays a BLOB locator; NVARCHAR2/NCHAR are reported as
// VARCHAR2/CHAR (the charset form is not exposed).
func columnType(rows sqldriver.Rows, i int) (string, valueKind) {
	name := ""
	if t, ok := rows.(sqldriver.RowsColumnTypeDatabaseTypeName); ok {
		name = t.ColumnTypeDatabaseTypeName(i)
	}
	length := int64(0)
	if t, ok := rows.(sqldriver.RowsColumnTypeLength); ok {
		length, _ = t.ColumnTypeLength(i)
	}
	sized := func(n string) string {
		if length > 0 {
			return fmt.Sprintf("%s(%d)", n, length)
		}
		return n
	}
	switch name {
	case "NUMBER":
		var p, s int64
		if t, ok := rows.(sqldriver.RowsColumnTypePrecisionScale); ok {
			p, s, _ = t.ColumnTypePrecisionScale(i)
		}
		return numberType(p, s)
	case "NCHAR", "VARCHAR", "VarNum", "NullStr":
		return sized("VARCHAR2"), kindText
	case "CHAR", "CHARZ":
		return sized("CHAR"), kindText
	case "IBFloat", "BFloat":
		return "BINARY_FLOAT", kindText
	case "IBDouble", "BDouble":
		return "BINARY_DOUBLE", kindText
	case "LongVarChar", "OCIClobLocator":
		return "CLOB", kindText
	case "LONG":
		return "LONG", kindText
	case "LongRaw", "LongVarRaw", "OCIBlobLocator":
		if name == "OCIBlobLocator" {
			return "JSON", kindJSON
		}
		return "BLOB", kindText
	case "RAW", "VarRaw":
		return "RAW", kindText
	case "DATE", "OCIDate":
		return "DATE", kindText
	case "TimeStampDTY", "TIMESTAMP":
		return "TIMESTAMP", kindText
	case "TimeStampTZ_DTY", "TIMESTAMPTZ", "TimeTZ":
		return "TIMESTAMP WITH TIME ZONE", kindText
	case "TimeStampLTZ_DTY", "TimeStampeLTZ":
		return "TIMESTAMP WITH LOCAL TIME ZONE", kindText
	case "IntervalYM_DTY", "IntervalYM":
		return "INTERVAL YEAR TO MONTH", kindText
	case "IntervalDS_DTY", "IntervalDS":
		return "INTERVAL DAY TO SECOND", kindText
	case "ROWID":
		return "ROWID", kindText
	case "UROWID":
		return "UROWID", kindText
	case "XMLType", "OCIXMLType":
		return "XMLTYPE", kindText
	case "REFCURSOR", "ResultSet":
		return "REF CURSOR", kindText
	case "OCIFileLocator":
		return "BFILE", kindText
	case "TNSType(252)":
		return "BOOLEAN", kindText
	case "TNSType(127)":
		return "VECTOR", kindText
	}
	return name, kindText
}

// numberType maps go-ora's precision/scale: unconstrained NUMBER (and FLOAT,
// which go-ora cannot tell apart) report scale 255.
func numberType(p, s int64) (string, valueKind) {
	switch {
	case p <= 0 || s < 0 || s > 127:
		return "NUMBER", kindNumber
	case s == 0:
		return fmt.Sprintf("NUMBER(%d)", p), kindInt
	default:
		return fmt.Sprintf("NUMBER(%d,%d)", p, s), kindDecimal
	}
}

// normalize converts a go-ora value to one of the types listed in
// driver.Result.
func normalize(v any, kind valueKind) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		switch kind {
		case kindInt, kindNumber:
			return number(x, true)
		case kindDecimal:
			return number(x, false)
		case kindJSON:
			return jsonValue([]byte(x))
		}
		return x
	case []byte:
		if kind == kindJSON {
			return jsonValue(x)
		}
		return x
	case float32:
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		if math.IsInf(float64(x), 0) || math.IsNaN(float64(x)) {
			return float64(x)
		}
		return f
	case float64, int64, bool, time.Time:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// number turns go-ora's NUMBER text into int64 when allowed and exact, else
// driver.Number. go-ora renders fractions below one without a leading zero
// on some paths; it is added back.
func number(s string, integral bool) any {
	if integral && !strings.ContainsAny(s, ".eE") {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	switch {
	case strings.HasPrefix(s, "."):
		s = "0" + s
	case strings.HasPrefix(s, "-."):
		s = "-0" + s[1:]
	}
	return driver.Number(s)
}

func jsonValue(b []byte) driver.JSON {
	var buf bytes.Buffer
	if json.Compact(&buf, b) == nil {
		return driver.JSON(buf.String())
	}
	return driver.JSON(b)
}

// result streams the rows of a query, or holds the outcome of any other
// statement.
type result struct {
	s        *session
	text     string // as sent, to target cancellation
	rows     sqldriver.Rows
	dest     []sqldriver.Value
	cols     []driver.Column
	kinds    []valueKind
	err      error
	done     bool // rows fully read and closed
	closed   bool
	affected int64
	tag      string
	count    int64
	verb     string
}

func (r *result) Columns() []driver.Column { return r.cols }
func (r *result) Affected() int64          { return r.affected }

// Tag returns a command tag in the style of PostgreSQL ("INSERT 3",
// "CREATE TABLE", "SELECT 10" once all rows were read, "PL/SQL BLOCK").
func (r *result) Tag() string {
	if r.rows != nil {
		if !r.done {
			return ""
		}
		return fmt.Sprintf("%s %d", r.verb, r.count)
	}
	return r.tag
}

func newRows(s *session, text string, rows sqldriver.Rows) *result {
	r := &result{s: s, text: text, rows: rows, affected: -1, verb: "SELECT"}
	names := rows.Columns()
	nullable, _ := rows.(sqldriver.RowsColumnTypeNullable)
	for i, n := range names {
		typ, kind := columnType(rows, i)
		null := true
		if nullable != nil {
			null, _ = nullable.ColumnTypeNullable(i)
		}
		r.cols = append(r.cols, driver.Column{Name: n, Type: typ, Nullable: null})
		r.kinds = append(r.kinds, kind)
	}
	r.dest = make([]sqldriver.Value, len(names))
	return r
}

func (r *result) Next(ctx context.Context, n int) ([][]any, error) {
	n = max(n, 1)
	if r.done || r.closed || r.rows == nil {
		if r.err != nil {
			return nil, r.err
		}
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out [][]any
	err := r.s.call(ctx, r.text, func() error {
		for len(out) < n {
			if err := r.rows.Next(r.dest); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			row := make([]any, len(r.dest))
			for i, v := range r.dest {
				row[i] = normalize(v, r.kinds[i])
			}
			out = append(out, row)
		}
		return nil
	})
	r.count += int64(len(out))
	if err != nil || len(out) < n {
		r.finish()
		if err != nil {
			r.err = r.s.failure(err, r.text)
		}
	}
	if r.err != nil && len(out) == 0 {
		return nil, r.err
	}
	return out, nil
}

// finish closes the cursor and collects the output and transaction state.
func (r *result) finish() {
	if r.done {
		return
	}
	r.done = true
	if r.s.link.alive() {
		r.rows.Close()
		r.s.cur = nil
		r.s.poll(true)
	}
}

// Close discards the rest of the result: Oracle cursors are pulled, so
// closing one is a quick round trip and nothing needs cancelling.
func (r *result) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.rows != nil && !r.done {
		r.finish()
	}
	if r.s != nil && r.s.cur == r {
		r.s.cur = nil
	}
	return nil
}

// memResult is a result computed by the driver (SQL*Plus commands).
type memResult struct {
	cols []driver.Column
	rows [][]any
	tag  string
}

func (m *memResult) Columns() []driver.Column { return m.cols }
func (m *memResult) Affected() int64          { return -1 }
func (m *memResult) Tag() string              { return m.tag }
func (m *memResult) Close() error             { m.rows = nil; return nil }

func (m *memResult) Next(_ context.Context, n int) ([][]any, error) {
	n = min(max(n, 1), len(m.rows))
	out := m.rows[:n]
	m.rows = m.rows[n:]
	return out, nil
}
