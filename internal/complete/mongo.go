package complete

import (
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

func suggestMongo(r Request) Result {
	s, cur := r.Text, r.Cursor
	none := Result{Start: cur, End: cur}
	depth := 0 // open braces before the cursor
	for _, t := range lex(s, modeJS) {
		if t.start >= cur {
			break
		}
		switch {
		case t.kind == tString || t.kind == tComment:
			if t.containsCursor(cur) {
				return none
			}
		case t.isPunct(s, '{'):
			depth++
		case t.isPunct(s, '}'):
			depth = max(depth-1, 0)
		}
	}

	start, end := cur, cur
	for start > 0 && isIdentChar(s[start-1]) {
		start--
	}
	for end < len(s) && isIdentChar(s[end]) {
		end++
	}
	prefix := s[start:cur]
	before := s[:start]

	if chain, ok := strings.CutSuffix(before, "."); ok {
		k := identStart(chain)
		name := chain[k:]
		switch {
		case name == "db" && (k == 0 || chain[k-1] != '.'):
			rk := newRanker(prefix, Collection, Function)
			for _, o := range r.Objects {
				if o.Kind == driver.NodeCollection || o.Kind == driver.NodeTable {
					rk.add(Item{Label: o.Name, Insert: collectionRef(o.Name), Detail: o.Detail, Kind: Collection})
				}
			}
			addSigs(rk, mongoDBMethods, Function)
			return rk.result(start, end)
		case name != "" && k > 0 && chain[k-1] == '.' && isDB(chain[:k-1]):
			return sigResult(prefix, start, end, mongoMethods, Function)
		case strings.HasSuffix(chain, ")"):
			if callee(chain) == "getCollection" {
				return sigResult(prefix, start, end, mongoMethods, Function)
			}
			return sigResult(prefix, start, end, mongoCursorMethods, Function)
		}
		return Result{Start: start, End: end, Prefix: prefix}
	}

	switch {
	case depth > 0 && strings.HasPrefix(prefix, "$"):
		rk := newRanker(prefix, Keyword)
		addSigs(rk, mongoOperators, Keyword)
		return rk.result(start, end)
	case depth > 0:
		rk := newRanker(prefix, Column)
		if o := mongoCollection(r.Objects, s[:start]); o != nil {
			for _, c := range o.Columns {
				ins := c.Name
				if !isJSIdent(ins) {
					ins = strconv.Quote(ins)
				}
				rk.add(Item{Label: c.Name, Insert: ins, Detail: c.Type, Kind: Column})
			}
		}
		return rk.result(start, end)
	}
	if t := strings.TrimRight(before, " \t"); t == "" || strings.HasSuffix(t, "\n") || strings.HasSuffix(t, ";") {
		return sigResult(prefix, start, end, mongoStatementWords, Keyword)
	}
	return Result{Start: start, End: end, Prefix: prefix}
}

func addSigs(rk *ranker, sigs []sig, k Kind) {
	for _, f := range sigs {
		rk.add(Item{Label: f.name, Detail: f.detail, Kind: k})
	}
}

func sigResult(prefix string, start, end int, sigs []sig, k Kind) Result {
	rk := newRanker(prefix)
	addSigs(rk, sigs, k)
	return rk.result(start, end)
}

func identStart(s string) int {
	k := len(s)
	for k > 0 && isIdentChar(s[k-1]) {
		k--
	}
	return k
}

// isDB reports whether s ends with the bare identifier db.
func isDB(s string) bool {
	k := identStart(s)
	return s[k:] == "db" && (k == 0 || s[k-1] != '.')
}

// callee returns the name of the function whose call ends s, e.g.
// `db.getCollection("x")` → getCollection.
func callee(s string) string {
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			if depth--; depth == 0 {
				return s[identStart(s[:i]):i]
			}
		}
	}
	return ""
}

func isJSIdent(s string) bool {
	if s == "" || isDigit(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentChar(s[i]) || s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func collectionRef(name string) string {
	if isJSIdent(name) {
		return name
	}
	return "getCollection(" + strconv.Quote(name) + ")"
}

// mongoCollection finds the collection of the last `db.<name>.` before the
// cursor.
func mongoCollection(objs []driver.Object, before string) *driver.Object {
	i := strings.LastIndex(before, "db.")
	if i < 0 {
		return nil
	}
	rest := before[i+3:]
	n := 0
	for n < len(rest) && isIdentChar(rest[n]) {
		n++
	}
	for j := range objs {
		if o := &objs[j]; (o.Kind == driver.NodeCollection || o.Kind == driver.NodeTable) && o.Name == rest[:n] {
			return o
		}
	}
	return nil
}
