package mongo

import (
	"github.com/gxd3v/quarry/internal/driver"
)

// Split cuts a mongosh script into statements. A statement ends at a
// top-level semicolon, or at a line break when no bracket is open and the
// next line does not continue it (a line starting with ".", or a line
// ending with "." or ","). Statement text starts at its first non-space
// character (leading comments included), ends after its last token, and
// excludes the semicolon. Comment-only statements are dropped. Split never
// fails: malformed input is passed through for Execute to report.
func Split(script string) []driver.Statement {
	var (
		out      []driver.Statement
		start    = -1 // first token of the current statement
		codeEnd  = -1 // end of its last non-comment token
		depth    = 0
		prev     token
		hasPrev  bool
		newlines bool // a line break was seen since the last code token
		lastEnd  = 0  // end of the previous statement
	)
	flush := func() {
		if start >= 0 && codeEnd > start {
			out = append(out, driver.Statement{Text: script[start:codeEnd], Offset: start})
			lastEnd = codeEnd
		}
		start, codeEnd, depth, hasPrev, newlines = -1, -1, 0, false, false
	}
	i := 0
	for i < len(script) {
		if n, nl := space(script, i); n > 0 {
			if nl {
				newlines = true
			}
			i += n
			continue
		}
		kind, end, _ := scan(script, i, regexAllowedAfter(prev, hasPrev))
		text := script[i:end]
		if kind == tComment {
			// A comment trailing the previous statement on its line is not
			// part of the next one.
			if start < 0 && (len(out) == 0 || lineBreakBetween(script, lastEnd, i)) {
				start = i
			}
			if end > i && text[1] == '*' {
				for k := i; k < end; k++ {
					if script[k] == '\n' {
						newlines = true
					}
				}
			}
			i = end
			continue
		}
		// A line break at depth 0 ends the statement unless this token or
		// the previous one continues it.
		if newlines && codeEnd >= 0 && depth == 0 && !continues(prev, kind, text) {
			// Keep comments that precede this token with the next statement.
			s := codeEnd
			flush()
			start = firstToken(script, s, i)
		}
		newlines = false
		if kind == tPunct && text == ";" && depth == 0 {
			flush()
			lastEnd = end
			i = end
			continue
		}
		if start < 0 {
			start = i
		}
		if kind == tPunct {
			switch text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth = max(depth-1, 0)
			}
		}
		codeEnd = end
		prev, hasPrev = token{kind: kind, text: text}, true
		i = end
	}
	flush()
	return out
}

// continues reports whether a token after a line break continues the
// statement whose last token was prev.
func continues(prev token, kind tokKind, text string) bool {
	if kind == tPunct && (text == "." || text == ")" || text == "]" || text == "}" || text == ",") {
		return true
	}
	if prev.kind == tPunct {
		switch prev.text {
		case ".", ",", ":", "(", "[", "{", "+", "-", "*", "&", "|", "?", "=":
			return true
		}
	}
	return false
}

// firstToken returns the offset of the first non-space character in
// script[from:to], or to.
func firstToken(script string, from, to int) int {
	for i := from; i < to; {
		if n, _ := space(script, i); n > 0 {
			i += n
			continue
		}
		// Skip a trailing comment on the previous statement's line.
		kind, end, _ := scan(script, i, false)
		if kind == tComment && !lineBreakBetween(script, from, i) {
			i = end
			continue
		}
		return i
	}
	return to
}

func lineBreakBetween(s string, from, to int) bool {
	for i := from; i < to; i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return true
		}
	}
	return false
}

// StatementAt returns the statement under a cursor byte offset. A cursor
// inside a statement (or right after it) picks it; a cursor in the gap
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
			if !lineBreakBetween(script, prevEnd, offset) || lineBreakBetween(script, offset, min(st.Offset, len(script))) {
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
