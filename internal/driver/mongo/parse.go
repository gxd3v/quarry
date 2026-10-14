package mongo

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// stmtKind is the shape of a parsed statement.
type stmtKind uint8

const (
	kDB   stmtKind = iota // db.…
	kShow                 // show dbs | collections
	kUse                  // use <db>
)

// call is one method call of a db.… chain.
type call struct {
	name   string
	pos    int   // offset of the method name
	args   []any // parsed arguments
	argPos []int // offset of each argument
	end    int   // offset just past ')'
}

func (c *call) arg(i int) (any, bool) {
	if i < len(c.args) {
		return c.args[i], true
	}
	return nil, false
}

func (c *call) argAt(i int) int {
	if i < len(c.argPos) {
		return c.argPos[i]
	}
	return c.end - 1
}

// statement is a parsed mongosh statement.
type statement struct {
	kind    stmtKind
	word    string // show: what; use: database
	wordPos int

	sibling    string // db.getSiblingDB("x"): database override
	hasSibling bool
	coll       string // collection, "" for database methods
	collPos    int
	hasColl    bool
	calls      []call // the method, then cursor modifiers
}

// parseError is a syntax error at a byte offset of the statement.
type parseError = lexError

func errAt(pos int, format string, args ...any) error {
	return &parseError{pos: pos, msg: fmt.Sprintf(format, args...)}
}

type parser struct {
	src  string
	toks []token
	i    int
}

func (p *parser) peek() token {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return token{kind: tEOF, start: len(p.src), end: len(p.src)}
}

func (p *parser) next() token {
	t := p.peek()
	if p.i < len(p.toks) {
		p.i++
	}
	return t
}

func (p *parser) expect(punct string) (token, error) {
	t := p.peek()
	if !t.punct(punct) {
		return t, errAt(t.start, "expected %q, found %s", punct, describe(t))
	}
	return p.next(), nil
}

func describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of statement"
	case tPunct:
		return strconv.Quote(t.text)
	case tIdent:
		return "identifier " + t.text
	}
	txt := t.text
	if len(txt) > 30 {
		txt = txt[:27] + "…"
	}
	return t.kind.String() + " " + txt
}

// parseStatement parses one mongosh statement. A trailing semicolon is
// allowed; anything after it is an error.
func parseStatement(src string) (*statement, error) {
	if st, ok, err := parseShellCommand(src); ok || err != nil {
		return st, err
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	t := p.peek()
	if t.kind == tEOF {
		return nil, errAt(0, "empty statement")
	}
	if !t.is(tIdent, "db") {
		return nil, errAt(t.start, "expected a statement starting with db., show or use, found %s", describe(t))
	}
	p.next()
	st := &statement{kind: kDB}
	if err := p.chain(st); err != nil {
		return nil, err
	}
	for p.peek().punct(";") {
		p.next()
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, errAt(t.start, "unexpected %s (run one statement at a time)", describe(t))
	}
	return st, nil
}

// parseShellCommand recognises `show <what>` and `use <db>`, which are not
// JavaScript.
func parseShellCommand(src string) (*statement, bool, error) {
	head, ok := firstCode(src, 0)
	if !ok || head.kind != tIdent || (head.text != "show" && head.text != "use") {
		return nil, false, nil
	}
	if n, ok := firstCode(src, head.end); ok && n.kind == tPunct && (n.text == "." || n.text == "(" || n.text == "=" || n.text == "[") {
		return nil, false, nil
	}
	// The argument is the raw text up to the end of line or ';', so names
	// like my-db work.
	rest := src[head.end:]
	end := len(rest)
	if k := strings.IndexAny(rest, ";\n\r"); k >= 0 {
		end = k
	}
	if k := strings.Index(rest[:end], "//"); k >= 0 {
		end = k
	}
	if k := strings.Index(rest[:end], "/*"); k >= 0 {
		end = k
	}
	word := strings.TrimSpace(rest[:end])
	wordPos := head.end + strings.Index(rest, word)
	if word == "" {
		return nil, true, errAt(head.end, "%s: missing argument", head.text)
	}
	if strings.ContainsAny(word, " \t") {
		k := strings.IndexAny(word, " \t")
		return nil, true, errAt(wordPos+k, "%s takes one argument", head.text)
	}
	// Only comments, whitespace and semicolons may follow.
	tail := rest[end:]
	if tt, err := lex(tail); err == nil {
		for _, t := range tt {
			if !t.punct(";") {
				return nil, true, errAt(head.end+end+t.start, "unexpected %s (run one statement at a time)", describe(t))
			}
		}
	}
	if head.text == "use" {
		if len(word) >= 2 && (word[0] == '"' || word[0] == '\'') && word[len(word)-1] == word[0] {
			word = word[1 : len(word)-1]
		}
		return &statement{kind: kUse, word: word, wordPos: wordPos}, true, nil
	}
	switch word {
	case "dbs", "databases", "collections", "tables":
	default:
		return nil, true, errAt(wordPos, "unsupported: show %s (supported: show dbs, show collections)", word)
	}
	return &statement{kind: kShow, word: word, wordPos: wordPos}, true, nil
}

// firstCode returns the first non-comment token at or after from.
func firstCode(src string, from int) (token, bool) {
	for i := from; i < len(src); {
		if n, _ := space(src, i); n > 0 {
			i += n
			continue
		}
		kind, end, _ := scan(src, i, true)
		if kind == tComment {
			i = end
			continue
		}
		return token{kind: kind, start: i, end: end, text: src[i:end]}, true
	}
	return token{}, false
}

// chain parses the accessors after `db`.
func (p *parser) chain(st *statement) error {
	var parts []string // collection name parts, db.a.b.find → a.b
	partPos := -1
	for {
		t := p.peek()
		var name string
		var namePos int
		switch {
		case t.punct("."):
			p.next()
			id := p.next()
			if id.kind != tIdent {
				return errAt(id.start, "expected a name after '.', found %s", describe(id))
			}
			name, namePos = id.text, id.start
		case t.punct("[") && len(st.calls) == 0:
			p.next()
			s := p.next()
			if s.kind != tString {
				return errAt(s.start, "expected a collection name string, found %s", describe(s))
			}
			if _, err := p.expect("]"); err != nil {
				return err
			}
			if partPos < 0 {
				partPos = s.start
			}
			parts = append(parts, s.val)
			continue
		default:
			if len(st.calls) == 0 {
				if len(parts) > 0 {
					return errAt(t.start, "incomplete statement: call a method, e.g. db.%s.find()", strings.Join(parts, "."))
				}
				if st.hasColl {
					return errAt(t.start, "incomplete statement: call a method, e.g. .find()")
				}
				return errAt(t.start, "incomplete statement: expected db.<collection>.<method>(…)")
			}
			return nil
		}
		if !p.peek().punct("(") {
			if len(st.calls) > 0 {
				return errAt(namePos, "properties are not supported (%s); call a method", name)
			}
			if st.hasColl {
				st.coll += "." + name
			} else {
				if partPos < 0 {
					partPos = namePos
				}
				parts = append(parts, name)
			}
			continue
		}
		c, err := p.call(name, namePos)
		if err != nil {
			return err
		}
		if len(st.calls) == 0 && len(parts) > 0 && !st.hasColl {
			st.coll, st.collPos, st.hasColl = strings.Join(parts, "."), partPos, true
			parts = nil
		}
		if len(st.calls) == 0 && !st.hasColl {
			switch name {
			case "getCollection":
				s, err := stringArg(c, 0, "collection name")
				if err != nil {
					return err
				}
				st.coll, st.collPos, st.hasColl = s, c.argAt(0), true
				continue
			case "getSiblingDB":
				s, err := stringArg(c, 0, "database name")
				if err != nil {
					return err
				}
				if st.hasSibling {
					return errAt(namePos, "getSiblingDB can only be used once")
				}
				st.sibling, st.hasSibling = s, true
				continue
			}
		}
		st.calls = append(st.calls, c)
	}
}

func stringArg(c call, i int, what string) (string, error) {
	v, ok := c.arg(i)
	if !ok {
		return "", errAt(c.argAt(i), "%s: missing %s", c.name, what)
	}
	s, isStr := v.(string)
	if !isStr || s == "" {
		return "", errAt(c.argAt(i), "%s: %s must be a non-empty string", c.name, what)
	}
	return s, nil
}

// call parses `(args…)` after a method name.
func (p *parser) call(name string, pos int) (call, error) {
	c := call{name: name, pos: pos}
	if _, err := p.expect("("); err != nil {
		return c, err
	}
	for !p.peek().punct(")") {
		start := p.peek().start
		v, err := p.value()
		if err != nil {
			return c, err
		}
		c.args = append(c.args, v)
		c.argPos = append(c.argPos, start)
		t := p.peek()
		if t.punct(",") {
			p.next()
			continue
		}
		if !t.punct(")") {
			return c, errAt(t.start, "expected ',' or ')', found %s", describe(t))
		}
	}
	c.end = p.next().end
	return c, nil
}

// value parses one relaxed JS literal into a BSON-ready Go value: bson.D,
// bson.A, string, int32, int64, float64, bool, nil, bson.Regex,
// bson.ObjectID, bson.DateTime, bson.Decimal128, bson.Binary,
// bson.Timestamp, bson.MinKey, bson.MaxKey.
func (p *parser) value() (any, error) {
	t := p.peek()
	switch t.kind {
	case tEOF:
		return nil, errAt(t.start, "expected a value, found end of statement")
	case tString:
		p.next()
		return t.val, nil
	case tNumber:
		p.next()
		return parseNumber(t)
	case tRegex:
		p.next()
		return regex(t.val, t.flags, t.start)
	case tPunct:
		switch t.text {
		case "{":
			return p.object()
		case "[":
			return p.array()
		case "-", "+":
			p.next()
			n := p.peek()
			var v any
			switch {
			case n.kind == tNumber:
				p.next()
				var err error
				if v, err = parseNumber(n); err != nil {
					return nil, err
				}
			case n.is(tIdent, "Infinity"):
				p.next()
				v = math.Inf(1)
			case n.is(tIdent, "NaN"):
				p.next()
				v = math.NaN()
			default:
				return nil, errAt(n.start, "expected a number after %q", t.text)
			}
			if t.text == "-" {
				return negate(v), nil
			}
			return v, nil
		}
		return nil, errAt(t.start, "expected a value, found %s", describe(t))
	case tIdent:
		p.next()
		switch t.text {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null", "undefined":
			return nil, nil
		case "NaN":
			return math.NaN(), nil
		case "Infinity":
			return math.Inf(1), nil
		case "new":
			h := p.next()
			if h.kind != tIdent {
				return nil, errAt(h.start, "expected a constructor after new, found %s", describe(h))
			}
			return p.helper(h, true)
		}
		return p.helper(t, false)
	}
	return nil, errAt(t.start, "expected a value, found %s", describe(t))
}

func (p *parser) object() (any, error) {
	open := p.next()
	d := bson.D{}
	for {
		t := p.peek()
		if t.punct("}") {
			p.next()
			return d, nil
		}
		var key string
		switch t.kind {
		case tIdent:
			key = t.text
		case tString:
			key = t.val
		case tNumber:
			v, err := parseNumber(t)
			if err != nil {
				return nil, err
			}
			key = numberKey(v)
		case tEOF:
			return nil, errAt(open.start, "unclosed '{'")
		default:
			if t.punct("...") {
				return nil, errAt(t.start, "spread syntax is not supported")
			}
			if t.punct("[") {
				return nil, errAt(t.start, "computed keys are not supported")
			}
			return nil, errAt(t.start, "expected a field name or '}', found %s", describe(t))
		}
		p.next()
		if c := p.peek(); !c.punct(":") {
			if c.punct(",") || c.punct("}") {
				return nil, errAt(c.start, "expected ':' after %q (shorthand properties are not supported)", key)
			}
			return nil, errAt(c.start, "expected ':' after %q, found %s", key, describe(c))
		}
		p.next()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		// Like JavaScript, a repeated key keeps its first position and its
		// last value.
		if k := slices.IndexFunc(d, func(e bson.E) bool { return e.Key == key }); k >= 0 {
			d[k].Value = v
		} else {
			d = append(d, bson.E{Key: key, Value: v})
		}
		switch n := p.peek(); {
		case n.punct(","):
			p.next()
		case n.punct("}"):
		case n.kind == tEOF:
			return nil, errAt(open.start, "unclosed '{'")
		default:
			return nil, errAt(n.start, "expected ',' or '}', found %s", describe(n))
		}
	}
}

func numberKey(v any) string {
	switch n := v.(type) {
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

func (p *parser) array() (any, error) {
	open := p.next()
	a := bson.A{}
	for {
		t := p.peek()
		switch {
		case t.punct("]"):
			p.next()
			return a, nil
		case t.punct(","):
			return nil, errAt(t.start, "empty array elements are not supported")
		case t.kind == tEOF:
			return nil, errAt(open.start, "unclosed '['")
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		switch n := p.peek(); {
		case n.punct(","):
			p.next()
		case n.punct("]"):
		case n.kind == tEOF:
			return nil, errAt(open.start, "unclosed '['")
		default:
			return nil, errAt(n.start, "expected ',' or ']', found %s", describe(n))
		}
	}
}

// parseNumber converts a numeric literal: integers that fit in 32 bits
// become int32 (as mongosh stores them), larger ones int64, the rest
// float64.
func parseNumber(t token) (any, error) {
	s := strings.ReplaceAll(t.text, "_", "")
	big := strings.HasSuffix(s, "n")
	s = strings.TrimSuffix(s, "n")
	if len(s) > 1 && s[0] == '0' && strings.ContainsRune("xXoObB", rune(s[1])) {
		n, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			u, uerr := strconv.ParseUint(s[2:], map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]], 64)
			if uerr != nil {
				return nil, errAt(t.start, "invalid number %s", t.text)
			}
			return float64(u), nil
		}
		return narrow(n, big), nil
	}
	if !strings.ContainsAny(s, ".eE") {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return narrow(n, big), nil
		}
	}
	if big {
		return nil, errAt(t.start, "invalid BigInt %s", t.text)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !strings.Contains(err.Error(), "range") {
		return nil, errAt(t.start, "invalid number %s", t.text)
	}
	return f, nil
}

func narrow(n int64, big bool) any {
	if !big && n >= math.MinInt32 && n <= math.MaxInt32 {
		return int32(n)
	}
	return n
}

func negate(v any) any {
	switch n := v.(type) {
	case int32:
		if n == math.MinInt32 {
			return -int64(n)
		}
		return -n
	case int64:
		if n == -math.MinInt32 {
			return int32(math.MinInt32)
		}
		if n == math.MinInt64 {
			return -float64(n)
		}
		return -n
	case float64:
		return -n
	}
	return v
}

// regex builds a BSON regular expression from JS flags; g, y and d have no
// MongoDB meaning and are dropped.
func regex(pattern, flags string, pos int) (any, error) {
	var opts []byte
	for i := 0; i < len(flags); i++ {
		switch f := flags[i]; f {
		case 'i', 'm', 's', 'u', 'x', 'l':
			if !slices.Contains(opts, f) {
				opts = append(opts, f)
			}
		case 'g', 'y', 'd':
		default:
			return nil, errAt(pos, "invalid regular expression flag %q", string(f))
		}
	}
	slices.Sort(opts)
	return bson.Regex{Pattern: pattern, Options: string(opts)}, nil
}

// helper evaluates a shell helper such as ObjectId("…") or new Date().
func (p *parser) helper(name token, isNew bool) (any, error) {
	switch name.text {
	case "MinKey", "MaxKey":
		if p.peek().punct("(") {
			c, err := p.call(name.text, name.start)
			if err != nil {
				return nil, err
			}
			if len(c.args) > 0 {
				return nil, errAt(c.argAt(0), "%s takes no arguments", name.text)
			}
		}
		if name.text == "MinKey" {
			return bson.MinKey{}, nil
		}
		return bson.MaxKey{}, nil
	}
	if !helpers[name.text] {
		if p.peek().punct("(") {
			return nil, errAt(name.start, "unknown function %s", name.text)
		}
		return nil, errAt(name.start, "unknown identifier %s (variables are not supported)", name.text)
	}
	if !p.peek().punct("(") {
		return nil, errAt(name.end, "expected '(' after %s", name.text)
	}
	c, err := p.call(name.text, name.start)
	if err != nil {
		return nil, err
	}
	return evalHelper(c, isNew)
}

var helpers = map[string]bool{
	"ObjectId": true, "ObjectID": true, "ISODate": true, "Date": true,
	"NumberLong": true, "Long": true, "NumberInt": true, "Int32": true,
	"NumberDecimal": true, "Decimal128": true, "Double": true, "NumberDouble": true,
	"UUID": true, "BinData": true, "HexData": true, "Timestamp": true, "RegExp": true,
}

func evalHelper(c call, isNew bool) (any, error) {
	argErr := func(i int, format string, args ...any) error {
		return errAt(c.argAt(i), c.name+": "+format, args...)
	}
	maxArgs := func(n int) error {
		if len(c.args) > n {
			return argErr(n, "too many arguments")
		}
		return nil
	}
	switch c.name {
	case "ObjectId", "ObjectID":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		if len(c.args) == 0 {
			return bson.NewObjectID(), nil
		}
		s, ok := c.args[0].(string)
		if !ok {
			return nil, argErr(0, "expected a 24-character hex string")
		}
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return nil, argErr(0, "invalid ObjectId %q: expected 24 hex characters", s)
		}
		return id, nil
	case "ISODate":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		if len(c.args) == 0 {
			return bson.NewDateTimeFromTime(time.Now()), nil
		}
		s, ok := c.args[0].(string)
		if !ok {
			return nil, argErr(0, "expected a date string")
		}
		t, err := parseDate(s)
		if err != nil {
			return nil, argErr(0, "invalid date %q", s)
		}
		return bson.NewDateTimeFromTime(t), nil
	case "Date":
		// Date() and new Date() both yield the current date (mongosh's
		// Date() returns a string, which is rarely what a query wants).
		switch len(c.args) {
		case 0:
			return bson.NewDateTimeFromTime(time.Now()), nil
		case 1:
			switch v := c.args[0].(type) {
			case string:
				t, err := parseDate(v)
				if err != nil {
					return nil, argErr(0, "invalid date %q", v)
				}
				return bson.NewDateTimeFromTime(t), nil
			case bson.DateTime:
				return v, nil
			}
			if f, ok := toFloat(c.args[0]); ok {
				return bson.DateTime(int64(f)), nil
			}
			return nil, argErr(0, "expected milliseconds or a date string")
		}
		// new Date(year, month0, day, h, m, s, ms), in UTC.
		if len(c.args) > 7 {
			return nil, argErr(7, "too many arguments")
		}
		parts := [7]int{0, 0, 1, 0, 0, 0, 0}
		for i, a := range c.args {
			f, ok := toFloat(a)
			if !ok {
				return nil, argErr(i, "expected a number")
			}
			parts[i] = int(f)
		}
		t := time.Date(parts[0], time.Month(parts[1]+1), parts[2], parts[3], parts[4], parts[5], parts[6]*1e6, time.UTC)
		return bson.NewDateTimeFromTime(t), nil
	case "NumberLong", "Long":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		if len(c.args) == 0 {
			return int64(0), nil
		}
		switch v := c.args[0].(type) {
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, argErr(0, "invalid 64-bit integer %q", v)
			}
			return n, nil
		case int32:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			if v != math.Trunc(v) || math.Abs(v) > 1<<63 {
				return nil, argErr(0, "%v is not a 64-bit integer", v)
			}
			return int64(v), nil
		}
		return nil, argErr(0, "expected a number or numeric string")
	case "NumberInt", "Int32":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		if len(c.args) == 0 {
			return int32(0), nil
		}
		var n int64
		switch v := c.args[0].(type) {
		case string:
			x, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, argErr(0, "invalid 32-bit integer %q", v)
			}
			n = x
		case int32:
			n = int64(v)
		case int64:
			n = v
		case float64:
			n = int64(v) // JS truncates
		default:
			return nil, argErr(0, "expected a number or numeric string")
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			return nil, argErr(0, "%d does not fit in 32 bits", n)
		}
		return int32(n), nil
	case "NumberDecimal", "Decimal128":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		s := "0"
		if len(c.args) == 1 {
			switch v := c.args[0].(type) {
			case string:
				s = strings.TrimSpace(v)
			case int32, int64:
				s = fmt.Sprint(v)
			case float64:
				s = strconv.FormatFloat(v, 'g', -1, 64)
			default:
				return nil, argErr(0, "expected a number or numeric string")
			}
		}
		d, err := bson.ParseDecimal128(s)
		if err != nil {
			return nil, argErr(0, "invalid decimal %q", s)
		}
		return d, nil
	case "Double", "NumberDouble":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		if len(c.args) == 0 {
			return float64(0), nil
		}
		if s, ok := c.args[0].(string); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return nil, argErr(0, "invalid number %q", s)
			}
			return f, nil
		}
		if f, ok := toFloat(c.args[0]); ok {
			return f, nil
		}
		return nil, argErr(0, "expected a number")
	case "UUID":
		if err := maxArgs(1); err != nil {
			return nil, err
		}
		if len(c.args) == 0 {
			var b [16]byte
			rand.Read(b[:])
			b[6] = b[6]&0x0f | 0x40
			b[8] = b[8]&0x3f | 0x80
			return bson.Binary{Subtype: bson.TypeBinaryUUID, Data: b[:]}, nil
		}
		s, ok := c.args[0].(string)
		if !ok {
			return nil, argErr(0, "expected a UUID string")
		}
		b, err := parseUUID(s)
		if err != nil {
			return nil, argErr(0, "invalid UUID %q", s)
		}
		return bson.Binary{Subtype: bson.TypeBinaryUUID, Data: b}, nil
	case "BinData", "HexData":
		if len(c.args) != 2 {
			return nil, errAt(c.pos, "%s takes a subtype and a %s string", c.name, map[bool]string{true: "base64", false: "hex"}[c.name == "BinData"])
		}
		sub, ok := toFloat(c.args[0])
		if !ok || sub < 0 || sub > 255 || sub != math.Trunc(sub) {
			return nil, argErr(0, "subtype must be an integer between 0 and 255")
		}
		s, ok := c.args[1].(string)
		if !ok {
			return nil, argErr(1, "expected a string")
		}
		var data []byte
		var err error
		if c.name == "BinData" {
			data, err = base64.StdEncoding.DecodeString(s)
		} else {
			data, err = hex.DecodeString(s)
		}
		if err != nil {
			return nil, argErr(1, "invalid data: %v", err)
		}
		return bson.Binary{Subtype: byte(sub), Data: data}, nil
	case "Timestamp":
		switch len(c.args) {
		case 0:
			return bson.Timestamp{}, nil
		case 1:
			d, ok := c.args[0].(bson.D)
			if !ok {
				return nil, argErr(0, "expected {t: <seconds>, i: <increment>}")
			}
			var ts bson.Timestamp
			for _, e := range d {
				f, ok := toFloat(e.Value)
				if !ok || f < 0 || f > math.MaxUint32 {
					return nil, argErr(0, "%s must be an unsigned 32-bit integer", e.Key)
				}
				switch e.Key {
				case "t":
					ts.T = uint32(f)
				case "i":
					ts.I = uint32(f)
				default:
					return nil, argErr(0, "unknown field %q", e.Key)
				}
			}
			return ts, nil
		case 2:
			var v [2]uint32
			for i := range 2 {
				f, ok := toFloat(c.args[i])
				if !ok || f < 0 || f > math.MaxUint32 || f != math.Trunc(f) {
					return nil, argErr(i, "expected an unsigned 32-bit integer")
				}
				v[i] = uint32(f)
			}
			return bson.Timestamp{T: v[0], I: v[1]}, nil
		}
		return nil, argErr(2, "too many arguments")
	case "RegExp":
		if len(c.args) == 0 || len(c.args) > 2 {
			return nil, errAt(c.pos, "RegExp takes a pattern and optional flags")
		}
		var pattern string
		switch v := c.args[0].(type) {
		case string:
			pattern = v
		case bson.Regex:
			pattern = v.Pattern
		default:
			return nil, argErr(0, "expected a pattern string")
		}
		flags := ""
		if len(c.args) == 2 {
			s, ok := c.args[1].(string)
			if !ok {
				return nil, argErr(1, "expected a flags string")
			}
			flags = s
		}
		return regex(pattern, flags, c.argAt(1))
	}
	return nil, errAt(c.pos, "unknown function %s", c.name)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func parseUUID(s string) ([]byte, error) {
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return nil, fmt.Errorf("want 32 hex digits")
	}
	return hex.DecodeString(h)
}

// dateLayouts are the formats ISODate and new Date accept. Times without a
// zone are UTC, like mongosh's ISODate.
var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006-01",
	"2006",
	"20060102T150405Z0700",
	"20060102",
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	if t, err := time.Parse(time.RFC1123, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised date")
}

// parseValue parses text as a single relaxed JS literal (with trailing
// whitespace, comments or a semicolon allowed).
func parseValue(text string) (any, error) {
	toks, err := lex(text)
	if err != nil {
		return nil, err
	}
	p := &parser{src: text, toks: toks}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	for p.peek().punct(";") {
		p.next()
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, errAt(t.start, "unexpected %s after the value", describe(t))
	}
	return v, nil
}
