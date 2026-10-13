package postgres

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gxd3v/quarry/internal/driver"
)

// closeDrain is how long Close lets an unfinished result drain on its own
// before asking the server to cancel the query.
const closeDrain = 200 * time.Millisecond

type result struct {
	s         *session
	text      string
	rows      pgx.Rows
	cancel    context.CancelFunc
	cols      []driver.Column
	decs      []decoder
	pending   []any
	err       error
	done      bool // rows fully read and closed
	closed    bool
	affected  int64
	tag       pgconn.CommandTag
	txAtStart bool
}

func (r *result) Columns() []driver.Column { return r.cols }
func (r *result) Affected() int64          { return r.affected }

// Tag returns the server's command tag ("CREATE TABLE", "UPDATE 3") once the
// statement has completed.
func (r *result) Tag() string { return r.tag.String() }

func (r *result) Next(ctx context.Context, n int) ([][]any, error) {
	n = max(n, 1)
	var out [][]any
	if r.pending != nil {
		out = append(out, r.pending)
		r.pending = nil
	}
	if !r.done && !r.closed {
		stop := context.AfterFunc(ctx, r.cancel)
		defer stop()
		for len(out) < n {
			if !r.rows.Next() {
				if err := r.finish(); err != nil {
					r.err = queryError(err, r.text)
				}
				break
			}
			out = append(out, r.decode(r.rows.RawValues()))
		}
	}
	if r.err != nil && len(out) == 0 {
		return nil, r.err
	}
	return out, nil
}

// finish closes the underlying rows and records the outcome.
func (r *result) finish() error {
	if r.done {
		return nil
	}
	r.done = true
	r.rows.Close()
	r.tag = r.rows.CommandTag()
	r.cancel()
	r.s.syncTx()
	return r.rows.Err()
}

// Close discards the rest of the result. Outside a transaction a result that
// does not drain quickly is cancelled server-side; inside one it is drained,
// since a cancel would abort the transaction.
func (r *result) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.pending = nil
	if !r.done {
		if !r.txAtStart {
			t := time.AfterFunc(closeDrain, r.cancel)
			defer t.Stop()
		}
		r.finish()
	}
	if r.s != nil && r.s.cur == r {
		r.s.cur = nil
	}
	return nil
}

// describe resolves column types and value decoders. Types pgx does not know
// (enums, composites, extension types) are looked up on the metadata
// connection.
func (r *result) describe(ctx context.Context, fields []pgconn.FieldDescription) {
	tm := r.s.pc.TypeMap()
	var unknown []uint32
	for _, f := range fields {
		if _, ok := tm.TypeForOID(f.DataTypeOID); !ok {
			unknown = append(unknown, f.DataTypeOID)
		}
	}
	var extra map[uint32]typeInfo
	if len(unknown) > 0 {
		extra = r.s.parent.typeInfos(ctx, r.s.db, unknown)
	}
	for _, f := range fields {
		r.cols = append(r.cols, driver.Column{Name: f.Name, Type: typeName(tm, f.DataTypeOID, extra), Nullable: true})
		r.decs = append(r.decs, newDecoder(tm, f.DataTypeOID, extra))
	}
}

func (r *result) decode(raw [][]byte) []any {
	row := make([]any, len(r.decs))
	for i, b := range raw {
		if b != nil && i < len(row) {
			row[i] = r.decs[i](b)
		}
	}
	return row
}

type typeInfo struct {
	name  string
	array bool
	elem  uint32
	delim byte
}

// typeInfos returns what the catalog knows about oids, caching per database.
// Lookup failures are not fatal: callers fall back to text.
func (c *conn) typeInfos(ctx context.Context, db string, oids []uint32) map[uint32]typeInfo {
	c.typesMu.Lock()
	cache := c.types[db]
	if cache == nil {
		cache = map[uint32]typeInfo{}
		if c.types == nil {
			c.types = map[string]map[uint32]typeInfo{}
		}
		c.types[db] = cache
	}
	var missing []int64
	for _, o := range oids {
		if _, ok := cache[o]; !ok {
			missing = append(missing, int64(o))
		}
	}
	c.typesMu.Unlock()

	if len(missing) > 0 {
		const q = `
SELECT t.oid::int8, format_type(t.oid, NULL), t.typcategory = 'A', t.typelem::int8, coalesce(e.typdelim::text, ',')
FROM pg_type t
LEFT JOIN pg_type e ON e.oid = t.typelem
WHERE t.oid = ANY($1::int8[]::oid[])`
		found := map[uint32]typeInfo{}
		err := c.list(ctx, db, q, []any{missing}, func(r pgx.Rows) error {
			var oid, elem int64
			var ti typeInfo
			var delim string
			if err := r.Scan(&oid, &ti.name, &ti.array, &elem, &delim); err != nil {
				return err
			}
			ti.elem = uint32(elem)
			ti.delim = cmp.Or(delim, ",")[0]
			found[uint32(oid)] = ti
			return nil
		})
		if err == nil {
			c.typesMu.Lock()
			for o, ti := range found {
				cache[o] = ti
			}
			c.typesMu.Unlock()
		}
	}

	out := make(map[uint32]typeInfo, len(oids))
	c.typesMu.Lock()
	for _, o := range oids {
		if ti, ok := cache[o]; ok {
			out[o] = ti
		}
	}
	c.typesMu.Unlock()
	return out
}

func typeName(tm *pgtype.Map, oid uint32, extra map[uint32]typeInfo) string {
	if t, ok := tm.TypeForOID(oid); ok {
		if ac, ok := t.Codec.(*pgtype.ArrayCodec); ok && ac.ElementType != nil {
			return ac.ElementType.Name + "[]"
		}
		return t.Name
	}
	if ti, ok := extra[oid]; ok {
		return ti.name
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decoder turns a non-NULL text-format value into one of the value types
// listed in driver.Result.
type decoder func([]byte) any

func newDecoder(tm *pgtype.Map, oid uint32, extra map[uint32]typeInfo) decoder {
	switch oid {
	case pgtype.BoolOID:
		return func(b []byte) any { return len(b) > 0 && b[0] == 't' }
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID:
		return func(b []byte) any {
			if n, err := strconv.ParseInt(string(b), 10, 64); err == nil {
				return n
			}
			return string(b)
		}
	case pgtype.Float4OID, pgtype.Float8OID:
		return func(b []byte) any {
			if f, err := strconv.ParseFloat(string(b), 64); err == nil {
				return f
			}
			return string(b)
		}
	case pgtype.NumericOID:
		return func(b []byte) any {
			switch s := string(b); s {
			case "NaN", "Infinity", "-Infinity":
				return s
			default:
				return driver.Number(s)
			}
		}
	case pgtype.JSONOID, pgtype.JSONBOID:
		return func(b []byte) any {
			var buf bytes.Buffer
			if json.Compact(&buf, b) == nil {
				return driver.JSON(buf.String())
			}
			return driver.JSON(b)
		}
	case pgtype.ByteaOID:
		return func(b []byte) any {
			var v []byte
			if tm.Scan(oid, pgtype.TextFormatCode, b, &v) == nil {
				return v
			}
			return string(b)
		}
	case pgtype.DateOID:
		return timeDecoder(tm, oid, func(v *pgtype.Date) (time.Time, bool) {
			return v.Time, v.Valid && v.InfinityModifier == pgtype.Finite
		})
	case pgtype.TimestampOID:
		return timeDecoder(tm, oid, func(v *pgtype.Timestamp) (time.Time, bool) {
			return v.Time, v.Valid && v.InfinityModifier == pgtype.Finite
		})
	case pgtype.TimestamptzOID:
		return timeDecoder(tm, oid, func(v *pgtype.Timestamptz) (time.Time, bool) {
			return v.Time, v.Valid && v.InfinityModifier == pgtype.Finite
		})
	}
	if t, ok := tm.TypeForOID(oid); ok {
		if ac, ok := t.Codec.(*pgtype.ArrayCodec); ok && ac.ElementType != nil {
			return arrayDecoder(newDecoder(tm, ac.ElementType.OID, extra), cmp.Or(ac.Delimiter, ','))
		}
	} else if ti, ok := extra[oid]; ok && ti.array && ti.elem != 0 {
		return arrayDecoder(newDecoder(tm, ti.elem, extra), ti.delim)
	}
	return func(b []byte) any { return string(b) }
}

// timeDecoder decodes date/timestamp types; infinities and anything pgx
// cannot parse (e.g. BC dates) are returned as text.
func timeDecoder[T any](tm *pgtype.Map, oid uint32, get func(*T) (time.Time, bool)) decoder {
	return func(b []byte) any {
		var v T
		if tm.Scan(oid, pgtype.TextFormatCode, b, &v) == nil {
			if t, ok := get(&v); ok {
				return t
			}
		}
		return string(b)
	}
}

func arrayDecoder(elem decoder, delim byte) decoder {
	return func(b []byte) any {
		v, err := parseArray(string(b), delim, elem)
		if err != nil {
			return string(b)
		}
		return v
	}
}

var errArray = errors.New("malformed array literal")

// parseArray parses PostgreSQL's array output ("{1,NULL,\"a b\"}", nested
// for multiple dimensions, optionally prefixed by "[l:u]=" bounds).
func parseArray(s string, delim byte, elem decoder) ([]any, error) {
	if strings.HasPrefix(s, "[") {
		i := strings.IndexByte(s, '=')
		if i < 0 {
			return nil, errArray
		}
		s = s[i+1:]
	}
	p := arrayParser{s: s, delim: delim, elem: elem}
	v, err := p.parse()
	if err != nil {
		return nil, err
	}
	if p.pos != len(s) {
		return nil, errArray
	}
	return v, nil
}

type arrayParser struct {
	s     string
	pos   int
	delim byte
	elem  decoder
}

func (p *arrayParser) skipSpace() {
	for p.pos < len(p.s) && isSpace(p.s[p.pos]) {
		p.pos++
	}
}

func (p *arrayParser) parse() ([]any, error) {
	p.skipSpace()
	if p.pos >= len(p.s) || p.s[p.pos] != '{' {
		return nil, errArray
	}
	p.pos++
	out := []any{}
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == '}' {
		p.pos++
		return out, nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.s) {
			return nil, errArray
		}
		switch p.s[p.pos] {
		case '{':
			v, err := p.parse()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		case '"':
			v, err := p.quoted()
			if err != nil {
				return nil, err
			}
			out = append(out, p.elem([]byte(v)))
		default:
			v, escaped := p.unquoted()
			if !escaped && strings.EqualFold(v, "NULL") {
				out = append(out, nil)
			} else {
				out = append(out, p.elem([]byte(v)))
			}
		}
		p.skipSpace()
		if p.pos >= len(p.s) {
			return nil, errArray
		}
		c := p.s[p.pos]
		p.pos++
		switch c {
		case '}':
			return out, nil
		case p.delim:
		default:
			return nil, errArray
		}
	}
}

func (p *arrayParser) quoted() (string, error) {
	var b strings.Builder
	p.pos++ // opening quote
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch c {
		case '\\':
			if p.pos+1 >= len(p.s) {
				return "", errArray
			}
			b.WriteByte(p.s[p.pos+1])
			p.pos += 2
			continue
		case '"':
			p.pos++
			return b.String(), nil
		}
		b.WriteByte(c)
		p.pos++
	}
	return "", errArray
}

func (p *arrayParser) unquoted() (string, bool) {
	var b strings.Builder
	escaped := false
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == p.delim || c == '}' {
			break
		}
		if c == '\\' && p.pos+1 < len(p.s) {
			b.WriteByte(p.s[p.pos+1])
			p.pos += 2
			escaped = true
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
	return strings.TrimRightFunc(b.String(), func(r rune) bool { return r < 0x80 && isSpace(byte(r)) }), escaped
}
