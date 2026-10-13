package sqlsafe

import "strings"

type tkind uint8

const (
	tWord   tkind = iota // bare word or keyword
	tIdent               // "quoted identifier"
	tString              // any string literal, dollar quotes included
	tOpen
	tClose
	tSemi
	tDot
	tOther // numbers, operators, punctuation
)

type token struct {
	kind tkind
	raw  string
	low  string // lower-cased raw, words only
}

func (t token) is(words ...string) bool {
	if t.kind != tWord {
		return false
	}
	for _, w := range words {
		if t.low == w {
			return true
		}
	}
	return false
}

// lexSQL tokenizes PostgreSQL and Oracle text. Comments are dropped; strings
// (standard, E'...', dollar-quoted, Oracle q'[]') and quoted identifiers become
// single tokens. Unterminated constructs run to the end of the input.
func lexSQL(s string) []token {
	var out []token
	emit := func(k tkind, a, b int) {
		t := token{kind: k, raw: s[a:b]}
		if k == tWord {
			t.low = strings.ToLower(t.raw)
		}
		out = append(out, t)
	}
	for i := 0; i < len(s); {
		c := s[i]
		var peek byte
		if i+1 < len(s) {
			peek = s[i+1]
		}
		switch {
		case isSpace(c):
			i++
		case c == '-' && peek == '-':
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(s)
			}
		case c == '/' && peek == '*':
			i = skipBlockComment(s, i)
		case c == '\'' || c == '"':
			j := skipString(s, i+1, false)
			if c == '"' {
				emit(tIdent, i, j)
			} else {
				emit(tString, i, j)
			}
			i = j
		case c == '$':
			j := i + 1
			if tag, ok := dollarTag(s, i); ok {
				j = len(s)
				if k := strings.Index(s[i+len(tag):], tag); k >= 0 {
					j = i + 2*len(tag) + k
				}
				emit(tString, i, j)
			} else {
				emit(tOther, i, j)
			}
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			if j < len(s) && s[j] == '\'' {
				switch strings.ToLower(s[i:j]) {
				case "e":
					k := skipString(s, j+1, true)
					emit(tString, i, k)
					i = k
					continue
				case "q", "nq":
					k := skipQQuote(s, j)
					emit(tString, i, k)
					i = k
					continue
				}
			}
			emit(tWord, i, j)
			i = j
		case '0' <= c && c <= '9':
			j := i + 1
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			emit(tOther, i, j)
			i = j
		case c == '(':
			emit(tOpen, i, i+1)
			i++
		case c == ')':
			emit(tClose, i, i+1)
			i++
		case c == ';':
			emit(tSemi, i, i+1)
			i++
		case c == '.':
			emit(tDot, i, i+1)
			i++
		default:
			emit(tOther, i, i+1)
			i++
		}
	}
	return out
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c == '$' || ('0' <= c && c <= '9')
}

// skipString returns the index just past the closing quote of a literal whose
// body starts at i; the quote is s[i-1]. Doubled quotes are escapes, and so
// are backslashes when backslash is set (E'...' strings).
func skipString(s string, i int, backslash bool) int {
	q := s[i-1]
	for i < len(s) {
		switch s[i] {
		case '\\':
			if backslash {
				i += 2
				continue
			}
		case q:
			if i+1 < len(s) && s[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(s)
}

// skipQQuote skips an Oracle q'<delim>...<delim>' literal whose quote is s[j].
func skipQQuote(s string, j int) int {
	if j+1 >= len(s) {
		return len(s)
	}
	end := s[j+1]
	switch end {
	case '[':
		end = ']'
	case '(':
		end = ')'
	case '{':
		end = '}'
	case '<':
		end = '>'
	}
	if k := strings.Index(s[j+2:], string([]byte{end, '\''})); k >= 0 {
		return j + 2 + k + 2
	}
	return len(s)
}

// skipBlockComment returns the index just past a (possibly nested) /* */
// comment starting at i.
func skipBlockComment(s string, i int) int {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}

// dollarTag reports the opening tag ($$ or $tag$) of a dollar-quoted string
// starting at i.
func dollarTag(s string, i int) (string, bool) {
	j := i + 1
	if j < len(s) && s[j] != '$' && !isIdentStart(s[j]) {
		return "", false
	}
	for j < len(s) && s[j] != '$' {
		if !isIdentChar(s[j]) {
			return "", false
		}
		j++
	}
	if j >= len(s) {
		return "", false
	}
	return s[i : j+1], true
}

// topLevel returns the indices of tokens at parenthesis depth 0, opening
// parentheses of depth-0 groups included.
func topLevel(toks []token) []int {
	var idx []int
	d := 0
	for i, t := range toks {
		switch t.kind {
		case tOpen:
			if d == 0 {
				idx = append(idx, i)
			}
			d++
		case tClose:
			d = max(d-1, 0)
		default:
			if d == 0 {
				idx = append(idx, i)
			}
		}
	}
	return idx
}

// closing returns the index of the parenthesis matching the one at i, or
// len(toks) when it is unbalanced.
func closing(toks []token, i int) int {
	d := 0
	for j := i; j < len(toks); j++ {
		switch toks[j].kind {
		case tOpen:
			d++
		case tClose:
			d--
			if d == 0 {
				return j
			}
		}
	}
	return len(toks)
}

// segmentEnd returns the index of the first depth-0 semicolon, or len(toks).
func segmentEnd(toks []token) int {
	d := 0
	for i, t := range toks {
		switch t.kind {
		case tOpen:
			d++
		case tClose:
			d = max(d-1, 0)
		case tSemi:
			if d == 0 {
				return i
			}
		}
	}
	return len(toks)
}

func firstWord(toks []token) int {
	for i, t := range toks {
		if t.kind == tWord {
			return i
		}
	}
	return -1
}

// name reads a possibly qualified object name starting at j and returns it
// with the index just past it.
func name(toks []token, j int) (string, int) {
	var parts []string
	for j < len(toks) && (toks[j].kind == tWord || toks[j].kind == tIdent) {
		parts = append(parts, toks[j].raw)
		j++
		if j < len(toks) && toks[j].kind == tDot {
			j++
			continue
		}
		break
	}
	return strings.Join(parts, "."), j
}

// skip advances j past the word sequence seq if it is there.
func skip(toks []token, j int, seq ...string) int {
	if j+len(seq) > len(toks) {
		return j
	}
	for k, w := range seq {
		if !toks[j+k].is(w) {
			return j
		}
	}
	return j + len(seq)
}
