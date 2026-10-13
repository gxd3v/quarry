package complete

import (
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

type sqlCtx int

const (
	ctxStatement  sqlCtx = iota // start of a statement
	ctxTables                   // after FROM, JOIN, INTO, ...
	ctxAfterTable               // after a table reference: clause keywords
	ctxColumns                  // select list, WHERE, ON, SET, ...
	ctxOther
)

type tableRef struct {
	schema, name, alias string
	obj                 *driver.Object
}

type sqlState struct {
	r      Request
	s      string
	refs   []tableRef
	kw     func(string) string
	quoted bool // the word under the cursor starts with a double quote
}

func suggestSQL(r Request) Result {
	s, cur := r.Text, r.Cursor
	none := Result{Start: cur, End: cur}

	var code []token // everything but comments
	for _, t := range lex(s, modeSQL) {
		switch t.kind {
		case tString, tComment:
			if t.containsCursor(cur) {
				return none
			}
		case tNumber:
			if t.start < cur && cur <= t.end {
				return none
			}
		}
		if t.kind != tComment {
			code = append(code, t)
		}
	}

	// The word under the cursor and the index of the first token at or
	// after it.
	x := &sqlState{r: r, s: s}
	start, end, prefix := cur, cur, ""
	pos := len(code)
	for i, t := range code {
		if t.kind == tIdent && t.start <= cur && cur <= t.end ||
			t.kind == tQuoted && t.start <= cur && (cur < t.end || t.open && cur <= t.end) {
			start, end, pos = t.start, t.end, i
			if t.kind == tQuoted {
				x.quoted = true
				if cur > t.start {
					prefix = unquote(s[t.start+1 : cur])
				}
			} else {
				prefix = s[t.start:cur]
			}
			break
		}
		if t.start >= cur {
			pos = i
			break
		}
	}

	st, en := 0, len(code)
	for i := pos - 1; i >= 0; i-- {
		if code[i].isPunct(s, ';') {
			st = i + 1
			break
		}
	}
	for i := pos; i < len(code); i++ {
		if code[i].isPunct(s, ';') {
			en = i
			break
		}
	}
	stmt := code[st:en]

	// Qualifier chain right before the word: a.b.|
	j, next := pos-st, start
	var qual []string
	for j >= 2 && stmt[j-1].isPunct(s, '.') && stmt[j-1].end == next &&
		isName(stmt[j-2]) && stmt[j-2].end == stmt[j-1].start {
		qual = append([]string{stmt[j-2].name(s)}, qual...)
		next = stmt[j-2].start
		j -= 2
	}
	before := stmt[:j]

	lower, ok := lowerTyped(prefix)
	if !ok || x.quoted {
		lower = lastKeywordLower(code[:st+j], s)
	}
	x.kw = caser(lower)
	x.refs = parseRefs(stmt, s)
	for i := range x.refs {
		x.refs[i].obj = x.lookup(x.refs[i].schema, x.refs[i].name)
	}

	var rk *ranker
	if len(qual) > 0 {
		rk = newRanker(prefix, Column, Table, View, Function)
		x.qualified(qual, rk)
		return rk.result(start, end)
	}
	switch classify(before, s) {
	case ctxStatement:
		rk = newRanker(prefix, Keyword)
		x.keywords(rk, stmtKeywords)
	case ctxTables:
		rk = newRanker(prefix, Table, View, Schema)
		x.tables(rk)
		x.schemas(rk)
	case ctxAfterTable:
		rk = newRanker(prefix, Keyword)
		x.keywords(rk, afterTableKeywords)
	case ctxColumns:
		rk = newRanker(prefix, Column, Alias, Function, Keyword, Table, View)
		x.columns(rk)
		x.functions(rk)
		x.keywords(rk, keywords)
	default:
		rk = newRanker(prefix, Keyword, Function, Table, View)
		x.keywords(rk, keywords)
		x.functions(rk)
		x.tables(rk)
	}
	return rk.result(start, end)
}

func isName(t token) bool { return t.kind == tIdent || t.kind == tQuoted }

// up returns the upper-cased text of an identifier token, or "".
func up(t token, s string) string {
	if t.kind != tIdent {
		return ""
	}
	return strings.ToUpper(t.text(s))
}

func lastKeywordLower(toks []token, s string) bool {
	for i := len(toks) - 1; i >= 0; i-- {
		if k := up(toks[i], s); reserved[k] {
			lower, _ := lowerTyped(toks[i].text(s))
			return lower
		}
	}
	return false
}

// classify finds the clause the cursor is in from the tokens before it.
func classify(before []token, s string) sqlCtx {
	n := len(before)
	if n == 0 {
		return ctxStatement
	}
	prev := before[n-1]
	switch up(prev, s) {
	case "EXPLAIN", "UNION", "INTERSECT", "EXCEPT":
		return ctxStatement
	case "ANALYZE", "VERBOSE", "ALL":
		if n >= 2 {
			switch up(before[n-2], s) {
			case "EXPLAIN", "ANALYZE", "UNION":
				return ctxStatement
			}
		}
	}
	if prev.isPunct(s, '(') && n >= 2 && up(before[n-2], s) == "AS" {
		return ctxStatement // WITH x AS (|
	}
	depth, paren := 0, false
	for i := n - 1; i >= 0; i-- {
		t := before[i]
		switch {
		case t.isPunct(s, ')'):
			depth++
			continue
		case t.isPunct(s, '('):
			if depth > 0 {
				depth--
			} else {
				paren = true
			}
			continue
		case depth > 0:
			continue
		}
		switch k := up(t, s); k {
		case "FROM", "JOIN", "INTO", "UPDATE", "TABLE", "DESCRIBE", "TRUNCATE":
			switch {
			case i == n-1, k == "FROM" && prev.isPunct(s, ',') && !paren:
				return ctxTables
			case k == "INTO" && paren:
				return ctxColumns // INSERT INTO t (|
			case paren:
				return ctxOther
			}
			return ctxAfterTable
		case "SELECT", "WHERE", "ON", "BY", "HAVING", "SET", "RETURNING",
			"AND", "OR", "CASE", "WHEN", "THEN", "ELSE":
			return ctxColumns
		case "VALUES", "LIMIT", "OFFSET", "INSERT", "DELETE", "CREATE", "ALTER", "DROP", "WITH":
			return ctxOther
		}
	}
	return ctxOther
}

// parseRefs collects the table references (with aliases) of a statement.
func parseRefs(stmt []token, s string) []tableRef {
	var refs []tableRef
	for i := 0; i < len(stmt); i++ {
		switch k := up(stmt[i], s); k {
		case "FROM", "JOIN", "UPDATE", "INTO", "TABLE", "DESCRIBE", "TRUNCATE":
			for {
				ref, n := parseRef(stmt[i+1:], s)
				if ref.name != "" {
					refs = append(refs, ref)
				}
				i += n
				if k != "FROM" || i+1 >= len(stmt) || !stmt[i+1].isPunct(s, ',') {
					break
				}
				i++
			}
		}
	}
	return refs
}

// parseRef reads `[schema.]name [AS] [alias]` and returns how many tokens
// it consumed.
func parseRef(ts []token, s string) (tableRef, int) {
	var ref tableRef
	i := 0
	for i < len(ts) {
		switch up(ts[i], s) {
		case "ONLY", "LATERAL", "IF", "NOT", "EXISTS":
			i++
			continue
		}
		break
	}
	notKeyword := func(t token) bool { return isName(t) && !reserved[up(t, s)] }
	if i < len(ts) && ts[i].isPunct(s, '(') { // subquery or VALUES list
		depth := 0
		for ; i < len(ts); i++ {
			if ts[i].isPunct(s, '(') {
				depth++
			} else if ts[i].isPunct(s, ')') {
				if depth--; depth == 0 {
					i++
					break
				}
			}
		}
	} else if i < len(ts) && notKeyword(ts[i]) {
		parts := []string{ts[i].name(s)}
		i++
		for i+1 < len(ts) && ts[i].isPunct(s, '.') && isName(ts[i+1]) {
			parts = append(parts, ts[i+1].name(s))
			i += 2
		}
		ref.name = parts[len(parts)-1]
		if len(parts) > 1 {
			ref.schema = parts[len(parts)-2]
		}
	} else {
		return ref, 0
	}
	if i < len(ts) && up(ts[i], s) == "AS" {
		i++
	}
	if i < len(ts) && notKeyword(ts[i]) {
		ref.alias = ts[i].name(s)
		i++
	}
	return ref, i
}

func isRelation(o *driver.Object) bool {
	return o.Kind == driver.NodeTable || o.Kind == driver.NodeView
}

// lookup resolves a table or view, following the search path when schema
// is empty.
func (x *sqlState) lookup(schema, name string) *driver.Object {
	objs := x.r.Objects
	find := func(sc string) *driver.Object {
		for i := range objs {
			o := &objs[i]
			if isRelation(o) && strings.EqualFold(o.Name, name) && strings.EqualFold(o.Schema, sc) {
				return o
			}
		}
		return nil
	}
	if schema != "" {
		return find(schema)
	}
	for _, sp := range x.r.SearchPath {
		if o := find(sp); o != nil {
			return o
		}
	}
	for i := range objs {
		if o := &objs[i]; isRelation(o) && strings.EqualFold(o.Name, name) {
			return o
		}
	}
	return nil
}

func (x *sqlState) isSchema(name string) bool {
	for _, o := range x.r.Objects {
		if o.Schema != "" && strings.EqualFold(o.Schema, name) {
			return true
		}
	}
	return false
}

// qualified offers what may follow `qual.`: columns of an alias or table,
// or the objects of a schema.
func (x *sqlState) qualified(qual []string, rk *ranker) {
	q := qual[len(qual)-1]
	if len(qual) >= 2 {
		x.columnsOf(x.lookup(qual[len(qual)-2], q), rk)
		return
	}
	for _, match := range []func(tableRef) bool{
		func(r tableRef) bool { return strings.EqualFold(r.alias, q) },
		func(r tableRef) bool { return r.obj != nil && strings.EqualFold(r.name, q) },
	} {
		for _, ref := range x.refs {
			if match(ref) {
				x.columnsOf(ref.obj, rk)
				return
			}
		}
	}
	if x.isSchema(q) {
		for _, o := range x.r.Objects {
			if !strings.EqualFold(o.Schema, q) {
				continue
			}
			if k, ok := objKind(o.Kind); ok {
				rk.add(Item{Label: o.Name, Insert: x.ident(o.Name), Detail: o.Detail, Kind: k})
			}
		}
		return
	}
	x.columnsOf(x.lookup("", q), rk)
}

func objKind(k driver.NodeKind) (Kind, bool) {
	switch k {
	case driver.NodeTable:
		return Table, true
	case driver.NodeView:
		return View, true
	case driver.NodeFunction:
		return Function, true
	}
	return 0, false
}

func (x *sqlState) columnsOf(o *driver.Object, rk *ranker) {
	if o == nil {
		return
	}
	for _, c := range o.Columns {
		rk.add(Item{Label: c.Name, Insert: x.ident(c.Name), Detail: c.Type, Kind: Column})
	}
}

// columns offers the columns of every table in the statement, qualified
// with the alias (or table name) when the name is ambiguous.
func (x *sqlState) columns(rk *ranker) {
	count := map[string]int{}
	for _, ref := range x.refs {
		if ref.obj != nil {
			for _, c := range ref.obj.Columns {
				count[strings.ToLower(c.Name)]++
			}
		}
	}
	for _, ref := range x.refs {
		if ref.alias != "" && !x.quoted {
			rk.add(Item{Label: ref.alias, Detail: ref.name, Kind: Alias})
		}
		if ref.obj == nil {
			continue
		}
		q := ref.alias
		if q == "" {
			q = x.ident(ref.name)
		}
		for _, c := range ref.obj.Columns {
			if count[strings.ToLower(c.Name)] > 1 {
				rk.add(Item{Label: q + "." + c.Name, Insert: q + "." + x.ident(c.Name),
					Detail: c.Type, Kind: Column}, c.Name)
			} else {
				rk.add(Item{Label: c.Name, Insert: x.ident(c.Name), Detail: c.Type, Kind: Column})
			}
		}
	}
}

// tables offers tables and views: unqualified when the search path resolves
// them, schema-qualified otherwise.
func (x *sqlState) tables(rk *ranker) {
	x.objects(rk, isRelation)
}

func (x *sqlState) objects(rk *ranker, keep func(*driver.Object) bool) {
	taken := map[string]bool{}
	add := func(o *driver.Object, unqualified bool) {
		k, _ := objKind(o.Kind)
		name := strings.ToLower(o.Name)
		if unqualified && !taken[name] {
			taken[name] = true
			rk.add(Item{Label: o.Name, Insert: x.ident(o.Name), Detail: o.Detail, Kind: k})
			return
		}
		rk.add(Item{Label: o.Schema + "." + o.Name, Insert: x.ident(o.Schema) + "." + x.ident(o.Name),
			Detail: o.Detail, Kind: k}, o.Name)
	}
	objs := x.r.Objects
	inPath := func(sc string) bool {
		for _, sp := range x.r.SearchPath {
			if strings.EqualFold(sp, sc) {
				return true
			}
		}
		return false
	}
	for _, sp := range x.r.SearchPath {
		for i := range objs {
			if o := &objs[i]; keep(o) && strings.EqualFold(o.Schema, sp) {
				add(o, true)
			}
		}
	}
	for i := range objs {
		if o := &objs[i]; keep(o) && !inPath(o.Schema) {
			add(o, o.Schema == "")
		}
	}
}

func (x *sqlState) schemas(rk *ranker) {
	for _, o := range x.r.Objects {
		if o.Schema != "" {
			rk.add(Item{Label: o.Schema, Insert: x.ident(o.Schema), Kind: Schema})
		}
	}
}

func (x *sqlState) functions(rk *ranker) {
	if x.quoted {
		return
	}
	for _, f := range sqlFunctions {
		rk.add(Item{Label: x.kw(f.name), Detail: f.detail, Kind: Function})
	}
	x.objects(rk, func(o *driver.Object) bool { return o.Kind == driver.NodeFunction })
}

func (x *sqlState) keywords(rk *ranker, words []string) {
	if x.quoted {
		return
	}
	for _, w := range words {
		rk.add(Item{Label: x.kw(w), Kind: Keyword})
	}
}

// ident quotes name when needed (or when the user started a quoted word).
func (x *sqlState) ident(name string) string {
	if x.quoted || needsQuote(name) {
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	return name
}

// needsQuote reports whether name is not a plain lower-case identifier or is
// a reserved word.
func needsQuote(name string) bool {
	if name == "" {
		return true
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c == '_' || i > 0 && isDigit(c)) {
			return true
		}
	}
	return reserved[strings.ToUpper(name)]
}
