package postgres

import (
	"strings"
	"unicode"

	"github.com/gxd3v/quarry/internal/driver"
)

type tokKind int

const (
	tokEOF tokKind = iota
	tokWord
	tokSemi
	tokOpen
	tokClose
	tokComment
	tokOther // literals, quoted identifiers, operators, numbers
)

// lexer walks a PostgreSQL script just far enough to find statement
// boundaries: it understands strings, quoted identifiers, dollar quotes and
// comments, and reports bare words so callers can spot keywords.
type lexer struct {
	s   string
	pos int
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c == '$' || ('0' <= c && c <= '9')
}

func (l *lexer) next() (k tokKind, start, end int) {
	s := l.s
	for l.pos < len(s) && isSpace(s[l.pos]) {
		l.pos++
	}
	start = l.pos
	if start >= len(s) {
		return tokEOF, start, start
	}
	c := s[start]
	var peek byte
	if start+1 < len(s) {
		peek = s[start+1]
	}
	switch {
	case c == '-' && peek == '-':
		if i := strings.IndexByte(s[start:], '\n'); i >= 0 {
			l.pos = start + i
		} else {
			l.pos = len(s)
		}
		return tokComment, start, l.pos
	case c == '/' && peek == '*':
		l.pos = skipBlockComment(s, start)
		return tokComment, start, l.pos
	case c == ';':
		l.pos++
		return tokSemi, start, l.pos
	case c == '(':
		l.pos++
		return tokOpen, start, l.pos
	case c == ')':
		l.pos++
		return tokClose, start, l.pos
	case c == '\'':
		l.pos = skipString(s, start+1, false)
		return tokOther, start, l.pos
	case c == '"':
		l.pos = skipString(s, start+1, false)
		return tokOther, start, l.pos
	case c == '$':
		if tag, ok := dollarTag(s, start); ok {
			if i := strings.Index(s[start+len(tag):], tag); i >= 0 {
				l.pos = start + len(tag) + i + len(tag)
			} else {
				l.pos = len(s)
			}
			return tokOther, start, l.pos
		}
		l.pos++
		return tokOther, start, l.pos
	case isIdentStart(c):
		i := start + 1
		for i < len(s) && isIdentChar(s[i]) {
			i++
		}
		if i == start+1 && (c == 'e' || c == 'E') && i < len(s) && s[i] == '\'' {
			l.pos = skipString(s, i+1, true)
			return tokOther, start, l.pos
		}
		l.pos = i
		return tokWord, start, i
	case '0' <= c && c <= '9':
		i := start + 1
		for i < len(s) && (isIdentChar(s[i]) || s[i] == '.') {
			i++
		}
		l.pos = i
		return tokOther, start, i
	}
	l.pos++
	return tokOther, start, l.pos
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// skipString returns the index just past the closing quote of a literal whose
// body starts at i. The quote char is s[i-1]; doubled quotes are escapes, and
// so are backslashes when backslash is set (E'...' strings).
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

// Split cuts a script into statements on top-level semicolons. Statement text
// starts at its first non-space character (leading comments included) and
// excludes the semicolon and trailing whitespace. Comment-only statements are
// dropped. Semicolons inside parentheses and inside BEGIN ATOMIC ... END
// routine bodies do not split, mirroring psql.
func Split(script string) []driver.Statement {
	var (
		out     []driver.Statement
		l       = lexer{s: script}
		start   = -1
		hasCode bool
		parens  int
		block   int
		words   []string
	)
	flush := func(end int) {
		if start >= 0 && hasCode {
			text := strings.TrimRightFunc(script[start:end], unicode.IsSpace)
			out = append(out, driver.Statement{Text: text, Offset: start})
		}
		start, hasCode, parens, block, words = -1, false, 0, 0, words[:0]
	}
	for {
		k, a, b := l.next()
		if k == tokEOF {
			flush(len(script))
			return out
		}
		if k == tokSemi && parens == 0 && block == 0 {
			flush(a)
			continue
		}
		if start < 0 {
			start = a
		}
		if k == tokComment {
			continue
		}
		hasCode = true
		switch k {
		case tokOpen:
			parens++
		case tokClose:
			parens = max(parens-1, 0)
		case tokWord:
			w := strings.ToLower(script[a:b])
			if len(words) < 4 {
				words = append(words, w)
			}
			if isRoutine(words) {
				switch {
				case w == "begin":
					block++
				case w == "case" && block > 0:
					block++
				case w == "end" && block > 0:
					block--
				}
			}
		}
	}
}

// isRoutine reports whether the leading words start CREATE [OR REPLACE]
// FUNCTION/PROCEDURE, whose SQL-standard bodies may contain semicolons.
func isRoutine(w []string) bool {
	routine := func(s string) bool { return s == "function" || s == "procedure" }
	switch {
	case len(w) >= 2 && w[0] == "create" && routine(w[1]):
		return true
	case len(w) >= 4 && w[0] == "create" && w[1] == "or" && w[2] == "replace" && routine(w[3]):
		return true
	}
	return false
}

// keywords returns the lower-cased bare words of a statement, skipping
// comments, literals and quoted identifiers.
func keywords(text string) []string {
	var out []string
	l := lexer{s: text}
	for {
		k, a, b := l.next()
		switch k {
		case tokEOF:
			return out
		case tokWord:
			out = append(out, strings.ToLower(text[a:b]))
		}
	}
}

// StatementAt returns the statement under a cursor byte offset. A cursor
// inside a statement (or on its semicolon) picks it; a cursor in the gap
// between two statements picks the previous one, unless it sits on the line
// where the next one starts. Before the first statement, the first one wins.
func StatementAt(stmts []driver.Statement, script string, offset int) (driver.Statement, bool) {
	if len(stmts) == 0 {
		return driver.Statement{}, false
	}
	offset = min(max(offset, 0), len(script))
	for i, st := range stmts {
		if offset < st.Offset {
			if i == 0 {
				return st, true
			}
			prev := stmts[i-1]
			prevEnd := min(prev.Offset+len(prev.Text), len(script))
			if !strings.Contains(script[prevEnd:offset], "\n") ||
				strings.Contains(script[offset:min(st.Offset, len(script))], "\n") {
				return prev, true
			}
			return st, true
		}
		if offset <= st.Offset+len(st.Text) {
			return st, true
		}
	}
	return stmts[len(stmts)-1], true
}
