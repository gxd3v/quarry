package complete

import "strings"

type tokKind int

const (
	tIdent tokKind = iota
	tQuoted
	tNumber
	tString
	tComment
	tPunct
)

type token struct {
	kind       tokKind
	start, end int
	open       bool // unterminated string, comment or quoted identifier
}

type lexMode int

const (
	modeSQL lexMode = iota
	modeJS
)

// lex splits s into tokens, skipping whitespace. It never fails: unterminated
// strings and comments run to the end of the input.
func lex(s string, mode lexMode) []token {
	var toks []token
	emit := func(k tokKind, a, b int, open bool) { toks = append(toks, token{k, a, b, open}) }
	i := 0
	for i < len(s) {
		c := s[i]
		next := byte(0)
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
		case mode == modeSQL && c == '-' && next == '-', mode == modeJS && c == '/' && next == '/':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			emit(tComment, i, i+j, true)
			i += j
		case c == '/' && next == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				emit(tComment, i, len(s), true)
				i = len(s)
			} else {
				emit(tComment, i, i+2+j+2, false)
				i += j + 4
			}
		case c == '\'' || mode == modeJS && (c == '"' || c == '`'):
			j, open := quoteEnd(s, i, c, mode == modeSQL)
			emit(tString, i, j, open)
			i = j
		case c == '"': // SQL quoted identifier
			j, open := quoteEnd(s, i, c, true)
			if nl := strings.IndexByte(s[i:], '\n'); open || nl >= 0 && i+nl < j {
				// Being typed: stop at the end of the word.
				j, open = i+1, true
				for j < len(s) && isIdentChar(s[j]) {
					j++
				}
			}
			emit(tQuoted, i, j, open)
			i = j
		case mode == modeSQL && c == '$' && dollarTag(s[i:]) != "":
			tag := dollarTag(s[i:])
			j := strings.Index(s[i+len(tag):], tag)
			if j < 0 {
				emit(tString, i, len(s), true)
				i = len(s)
			} else {
				end := i + len(tag) + j + len(tag)
				emit(tString, i, end, false)
				i = end
			}
		case isIdentStart(c) || mode == modeJS && c == '$':
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			emit(tIdent, i, j, false)
			i = j
		case isDigit(c) || c == '$' && isDigit(next):
			j := i + 1
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			emit(tNumber, i, j, false)
			i = j
		default:
			emit(tPunct, i, i+1, false)
			i++
		}
	}
	return toks
}

// quoteEnd returns the end of the quoted run starting at s[i]. Doubled quotes
// escape in SQL, backslashes in JS.
func quoteEnd(s string, i int, q byte, doubled bool) (int, bool) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case !doubled && s[j] == '\\':
			j++
		case s[j] == q:
			if doubled && j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, false
		}
	}
	return len(s), true
}

// dollarTag returns the PostgreSQL dollar-quote opener at the start of s
// ($$ or $tag$), or "".
func dollarTag(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	for j := 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '$':
			return s[:j+1]
		case isDigit(c) && j == 1, !isIdentChar(c) || c == '$':
			return ""
		}
	}
	return ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}

func isIdentChar(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

func (t token) text(s string) string { return s[t.start:t.end] }

func (t token) isPunct(s string, c byte) bool {
	return t.kind == tPunct && s[t.start] == c
}

// name returns the identifier a token denotes, unquoting "quoted" names.
func (t token) name(s string) string {
	if t.kind == tQuoted {
		end := t.end
		if !t.open {
			end--
		}
		return unquote(s[t.start+1 : max(end, t.start+1)])
	}
	return s[t.start:t.end]
}

func unquote(s string) string { return strings.ReplaceAll(s, `""`, `"`) }

// containsCursor reports whether cur lies strictly inside t (an unterminated
// token also contains its end).
func (t token) containsCursor(cur int) bool {
	return t.start < cur && (cur < t.end || t.open && cur <= t.end)
}
