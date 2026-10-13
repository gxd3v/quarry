package export

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

// Text is the canonical, untruncated text of a driver value, as CSV, TSV and
// Markdown write it: NULL is empty, bytes use PostgreSQL's \x hex format,
// times are "2006-01-02 15:04:05[.fraction][-07:00]" (offset only when not
// UTC), JSON is the raw document and arrays use PostgreSQL literal syntax.
func Text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return string(AppendText(nil, v))
}

// AppendText appends Text(v) to dst.
func AppendText(dst []byte, v any) []byte {
	switch v := v.(type) {
	case nil:
		return dst
	case string:
		return append(dst, v...)
	case bool:
		return strconv.AppendBool(dst, v)
	case int64:
		return strconv.AppendInt(dst, v, 10)
	case float64:
		return appendFloat(dst, v)
	case driver.Number:
		return append(dst, v...)
	case []byte:
		dst = append(dst, `\x`...)
		return hex.AppendEncode(dst, v)
	case time.Time:
		return appendTime(dst, v)
	case driver.JSON:
		return append(dst, v...)
	case []any:
		return appendArray(dst, v)
	}
	return fmt.Append(dst, v)
}

// appendFloat uses the shortest representation, without an exponent for
// normal magnitudes.
func appendFloat(dst []byte, f float64) []byte {
	switch {
	case math.IsInf(f, 1):
		return append(dst, "Infinity"...)
	case math.IsInf(f, -1):
		return append(dst, "-Infinity"...)
	}
	if a := math.Abs(f); a == 0 || (a >= 1e-6 && a < 1e21) {
		return strconv.AppendFloat(dst, f, 'f', -1, 64)
	}
	return strconv.AppendFloat(dst, f, 'g', -1, 64)
}

// timeLayout shows a fraction only when non-zero (ms, µs or ns precision).
func timeLayout(t time.Time) string {
	switch ns := t.Nanosecond(); {
	case ns == 0:
		return "2006-01-02 15:04:05"
	case ns%1e6 == 0:
		return "2006-01-02 15:04:05.000"
	case ns%1e3 == 0:
		return "2006-01-02 15:04:05.000000"
	}
	return "2006-01-02 15:04:05.000000000"
}

func appendTime(dst []byte, t time.Time) []byte {
	dst = t.AppendFormat(dst, timeLayout(t))
	if _, off := t.Zone(); off != 0 {
		dst = t.AppendFormat(dst, "-07:00")
	}
	return dst
}

func appendArray(dst []byte, a []any) []byte {
	dst = append(dst, '{')
	for i, e := range a {
		if i > 0 {
			dst = append(dst, ',')
		}
		switch e := e.(type) {
		case nil:
			dst = append(dst, "NULL"...)
		case []any:
			dst = appendArray(dst, e)
		default:
			dst = appendArrayElem(dst, Text(e))
		}
	}
	return append(dst, '}')
}

// appendArrayElem quotes an array element when PostgreSQL requires it.
func appendArrayElem(dst []byte, s string) []byte {
	quote := s == "" || strings.EqualFold(s, "NULL") || !utf8.ValidString(s)
	for i := 0; i < len(s) && !quote; i++ {
		switch s[i] {
		case '{', '}', ',', '"', '\\', ' ', '\t', '\n', '\r', '\v', '\f':
			quote = true
		}
	}
	if !quote {
		return append(dst, s...)
	}
	dst = append(dst, '"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			dst = append(dst, '\\')
		}
		dst = utf8.AppendRune(dst, r)
	}
	return append(dst, '"')
}

// JSONValue converts a driver value for JSON output: numbers stay numbers
// (Number as a raw JSON number), NaN and ±Inf become null, bytes become a
// "\x..." string, times RFC 3339 strings, valid JSON documents are embedded
// (compacted) and arrays become JSON arrays. The result marshals with
// encoding/json/v2; raw parts are jsontext.Value.
func JSONValue(v any) any {
	switch v := v.(type) {
	case nil, bool, int64, string:
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		return v
	case driver.Number:
		if isNumberLiteral(v) {
			return jsontext.Value(v)
		}
		return string(v) // NaN, Infinity
	case time.Time:
		return v.Format(time.RFC3339Nano)
	case driver.JSON:
		raw := jsontext.Value(v)
		if raw.Compact(jsonOpts...) == nil {
			return raw
		}
		return string(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = JSONValue(e)
		}
		return out
	}
	return Text(v)
}

var jsonOpts = []jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// isNumberLiteral reports whether s is a plain decimal number in JSON
// syntax (also valid SQL), as opposed to NaN or Infinity.
func isNumberLiteral[T ~string | ~[]byte](s T) bool {
	i := 0
	digits := func() bool {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i > start
	}
	if i < len(s) && s[i] == '-' {
		i++
	}
	if i < len(s) && s[i] == '0' {
		i++
	} else if !digits() {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if !digits() {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if !digits() {
			return false
		}
	}
	return i == len(s)
}

// encodeJSON writes JSONValue(v) to enc without intermediate allocations for
// the common scalar types. scratch is a reusable buffer.
func encodeJSON(enc *jsontext.Encoder, v any, scratch *[]byte) error {
	switch v := v.(type) {
	case nil:
		return enc.WriteToken(jsontext.Null)
	case bool:
		return enc.WriteToken(jsontext.Bool(v))
	case int64:
		return enc.WriteToken(jsontext.Int(v))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return enc.WriteToken(jsontext.Null)
		}
		return enc.WriteToken(jsontext.Float(v))
	case string:
		return enc.WriteToken(jsontext.String(v))
	case driver.Number:
		if isNumberLiteral(v) {
			*scratch = append((*scratch)[:0], v...)
			return enc.WriteValue(*scratch)
		}
		return enc.WriteToken(jsontext.String(string(v)))
	case time.Time:
		b := append((*scratch)[:0], '"')
		b = v.AppendFormat(b, time.RFC3339Nano)
		*scratch = append(b, '"')
		return enc.WriteValue(*scratch)
	case driver.JSON:
		*scratch = append((*scratch)[:0], v...)
		if enc.WriteValue(*scratch) == nil { // a failed write leaves enc unchanged
			return nil
		}
		return enc.WriteToken(jsontext.String(string(v)))
	case []any:
		if err := enc.WriteToken(jsontext.BeginArray); err != nil {
			return err
		}
		for _, e := range v {
			if err := encodeJSON(enc, e, scratch); err != nil {
				return err
			}
		}
		return enc.WriteToken(jsontext.EndArray)
	}
	*scratch = AppendText((*scratch)[:0], v)
	return enc.WriteToken(jsontext.String(string(*scratch)))
}

// SQLLiteral is v as a SQL literal for dialect "postgres", "oracle" or ""
// (generic: TRUE/FALSE, everything non-numeric as a quoted string of its
// Text). PostgreSQL times always carry their offset; Oracle gets 1/0
// booleans, HEXTORAW, TIMESTAMP literals and BINARY_DOUBLE_NAN/INFINITY.
func SQLLiteral(v any, dialect string) string {
	return string(AppendSQLLiteral(nil, v, dialect))
}

// AppendSQLLiteral appends SQLLiteral(v, dialect) to dst.
func AppendSQLLiteral(dst []byte, v any, dialect string) []byte {
	oracle := isOracle(dialect)
	switch v := v.(type) {
	case nil:
		return append(dst, "NULL"...)
	case bool:
		switch {
		case oracle && v:
			return append(dst, '1')
		case oracle:
			return append(dst, '0')
		case v:
			return append(dst, "TRUE"...)
		}
		return append(dst, "FALSE"...)
	case int64:
		return strconv.AppendInt(dst, v, 10)
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return appendFloat(dst, v)
		}
		if oracle {
			switch {
			case math.IsNaN(v):
				return append(dst, "BINARY_DOUBLE_NAN"...)
			case v > 0:
				return append(dst, "BINARY_DOUBLE_INFINITY"...)
			}
			return append(dst, "-BINARY_DOUBLE_INFINITY"...)
		}
	case driver.Number:
		if isNumberLiteral(v) {
			return append(dst, v...)
		}
	case []byte:
		if oracle {
			dst = append(dst, "HEXTORAW('"...)
			dst = hex.AppendEncode(dst, v)
			return append(dst, "')"...)
		}
	case time.Time:
		if oracle {
			dst = append(dst, "TIMESTAMP '"...)
			dst = v.AppendFormat(dst, timeLayout(v))
			if _, off := v.Zone(); off != 0 {
				dst = v.AppendFormat(dst, " -07:00")
			}
			return append(dst, '\'')
		}
		if isPostgres(dialect) {
			// Always explicit, or timestamptz would read it in the session zone.
			dst = append(dst, '\'')
			dst = v.AppendFormat(dst, timeLayout(v))
			dst = v.AppendFormat(dst, "-07:00")
			return append(dst, '\'')
		}
	}
	return appendSQLString(dst, v)
}

// appendSQLString appends Text(v) as a quoted SQL string, doubling quotes in
// place.
func appendSQLString(dst []byte, v any) []byte {
	dst = append(dst, '\'')
	mark := len(dst)
	dst = AppendText(dst, v)
	if n := bytes.Count(dst[mark:], []byte{'\''}); n > 0 {
		end := len(dst)
		dst = append(dst, make([]byte, n)...)
		for i, j := end-1, len(dst)-1; i >= mark; i-- {
			dst[j] = dst[i]
			j--
			if dst[i] == '\'' {
				dst[j] = '\''
				j--
			}
		}
	}
	return append(dst, '\'')
}

func isOracle(dialect string) bool   { return strings.EqualFold(dialect, string(driver.Oracle)) }
func isPostgres(dialect string) bool { return strings.EqualFold(dialect, string(driver.Postgres)) }

// QuoteIdent double-quotes an identifier unless it is a plain name that the
// dialect would not case-fold: lower-case for PostgreSQL and generic SQL,
// upper-case for Oracle.
func QuoteIdent(s, dialect string) string {
	oracle := isOracle(dialect)
	plain := s != ""
	for i, r := range s {
		letter := r >= 'a' && r <= 'z'
		if oracle {
			letter = r >= 'A' && r <= 'Z'
		}
		if !(r == '_' || letter || i > 0 && (r >= '0' && r <= '9' || r == '$' || oracle && r == '#')) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// UniqueNames returns the column names with duplicates suffixed (id, id_2,
// ...) so they can serve as JSON keys.
func UniqueNames(cols []driver.Column) []string {
	seen := make(map[string]bool, len(cols))
	out := make([]string, len(cols))
	for i, c := range cols {
		name := c.Name
		for n := 2; seen[name]; n++ {
			name = c.Name + "_" + strconv.Itoa(n)
		}
		seen[name] = true
		out[i] = name
	}
	return out
}
