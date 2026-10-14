package oracle

import (
	"slices"
	"strings"
	"unicode"

	"github.com/gxd3v/quarry/internal/driver"
)

type tokKind int

const (
	tokWord tokKind = iota
	tokSemi
	tokOpen
	tokClose
	tokSlash // a line holding only "/"
	tokComment
	tokOther // literals, quoted identifiers, operators, numbers
)

type token struct {
	kind       tokKind
	start, end int
	low        string // lower-cased text of words, or a punctuation character
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c == '$' || c == '#' || ('0' <= c && c <= '9')
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// lex tokenizes an Oracle script just far enough to find statement
// boundaries: strings (including q'[...]' and N'...'), quoted identifiers,
// comments and "/" terminator lines.
func lex(s string) []token {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		if isSpace(c) {
			i++
			continue
		}
		t := token{start: i, kind: tokOther}
		var peek byte
		if i+1 < len(s) {
			peek = s[i+1]
		}
		switch {
		case c == '-' && peek == '-':
			t.kind, i = tokComment, lineEnd(s, i)
		case c == '/' && peek == '*':
			t.kind = tokComment
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(s)
			}
		case c == '/' && aloneOnLine(s, i):
			t.kind, i = tokSlash, i+1
		case c == ';':
			t.kind, i = tokSemi, i+1
		case c == '(':
			t.kind, i = tokOpen, i+1
		case c == ')':
			t.kind, i = tokClose, i+1
		case c == '\'':
			i = skipString(s, i+1)
		case c == '"':
			if j := strings.IndexByte(s[i+1:], '"'); j >= 0 {
				i += j + 2
			} else {
				i = len(s)
			}
		case isIdentStart(c):
			if j, ok := literalPrefix(s, i); ok {
				i = j
				break
			}
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			t.kind, t.low, i = tokWord, strings.ToLower(s[i:j]), j
		case '0' <= c && c <= '9':
			j := i + 1
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			i = j
		default:
			t.low = s[i : i+1] // punctuation, for lookups such as "<<label>>"
			i++
		}
		t.end = i
		out = append(out, t)
	}
	return out
}

// literalPrefix recognizes N'...', Q'x...x' and NQ'x...x' literals starting
// at i and returns the index just past them.
func literalPrefix(s string, i int) (int, bool) {
	j := i
	if s[j] == 'n' || s[j] == 'N' {
		j++
	}
	if j < len(s) && (s[j] == 'q' || s[j] == 'Q') && j+2 < len(s) && s[j+1] == '\'' {
		return skipQQuote(s, j+2), true
	}
	if j > i && j < len(s) && s[j] == '\'' {
		return skipString(s, j+1), true
	}
	return 0, false
}

// skipString returns the index just past the closing quote of a '...'
// literal whose body starts at i; doubled quotes are escapes.
func skipString(s string, i int) int {
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(s)
}

// skipQQuote returns the index just past a q'x...x' literal whose delimiter
// is s[i].
func skipQQuote(s string, i int) int {
	open := s[i]
	end := open
	switch open {
	case '[':
		end = ']'
	case '{':
		end = '}'
	case '<':
		end = '>'
	case '(':
		end = ')'
	}
	if k := strings.Index(s[i+1:], string(end)+"'"); k >= 0 {
		return i + 1 + k + 2
	}
	return len(s)
}

func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(s)
}

func lineStart(s string, i int) int {
	return strings.LastIndexByte(s[:i], '\n') + 1
}

func blank(s string) bool {
	return strings.TrimLeft(s, " \t\r\f\v") == ""
}

// aloneOnLine reports whether the byte at i is the only non-blank character
// of its line.
func aloneOnLine(s string, i int) bool {
	return blank(s[lineStart(s, i):i]) && blank(s[i+1:lineEnd(s, i)])
}

// atLineStart reports whether t is the first token of its line.
func atLineStart(s string, t token) bool {
	return blank(s[lineStart(s, t.start):t.start])
}

// Split cuts a script into statements the way SQL*Plus and SQL Developer do:
//
//   - SQL statements end at a top-level semicolon, which is not part of the
//     statement text.
//   - PL/SQL units (DECLARE/BEGIN blocks; CREATE [OR REPLACE] PROCEDURE,
//     FUNCTION, PACKAGE [BODY], TRIGGER, TYPE [BODY], LIBRARY; WITH
//     FUNCTION queries) keep their semicolons. They end at a line holding
//     only "/", or, when the slash is missing, at the semicolon after the END
//     that closes the outermost block (not for WITH FUNCTION, compound
//     triggers and Java sources, which need the slash).
//   - A "/" line also ends a SQL statement, and is dropped when it follows
//     a terminated one.
//   - SQL*Plus line commands (SET SERVEROUTPUT, PROMPT, EXEC, DESC, ...) at
//     the start of a line end at the end of that line; REM lines are
//     comments.
//
// Statement text starts at its first non-space character (leading comments
// included) and has no trailing whitespace. Comment-only statements are
// dropped.
func Split(script string) []driver.Statement {
	toks := lex(script)
	var out []driver.Statement
	emit := func(start, end int) {
		text := strings.TrimRightFunc(script[start:end], unicode.IsSpace)
		if text != "" {
			out = append(out, driver.Statement{Text: text, Offset: start})
		}
	}
	for i := 0; i < len(toks); {
		start := toks[i].start
		j := i
		for j < len(toks) && toks[j].kind == tokComment {
			j++
		}
		if j == len(toks) {
			break
		}
		t := toks[j]
		switch {
		case t.kind == tokSemi || t.kind == tokSlash:
			i = j + 1
			continue
		case t.kind == tokWord && atLineStart(script, t) && (t.low == "rem" || t.low == "remark"):
			i = skipLine(script, toks, j)
			continue
		case t.kind == tokWord && atLineStart(script, t) && isSQLPlus(toks, j):
			end := lineEnd(script, t.start)
			emit(start, trimSemi(script, start, end))
			i = skipLine(script, toks, j)
			continue
		case t.kind == tokOther && script[t.start] == '@' && atLineStart(script, t):
			emit(start, lineEnd(script, t.start))
			i = skipLine(script, toks, j)
			continue
		}
		end, next := statementEnd(script, toks, j)
		emit(start, end)
		i = next
	}
	return out
}

// skipLine returns the index of the first token after the line of toks[j].
func skipLine(s string, toks []token, j int) int {
	end := lineEnd(s, toks[j].start)
	for j < len(toks) && toks[j].start < end {
		j++
	}
	return j
}

// trimSemi drops trailing semicolons (and blanks) from s[start:end].
func trimSemi(s string, start, end int) int {
	for end > start && (isSpace(s[end-1]) || s[end-1] == ';') {
		end--
	}
	return end
}

// statementEnd finds where the statement whose first code token is toks[j]
// ends: the end offset of its text and the index of the next token to scan.
func statementEnd(s string, toks []token, j int) (end, next int) {
	unit, heuristic := unitKind(toks[j:])
	if !unit {
		depth := 0
		for k := j; k < len(toks); k++ {
			switch toks[k].kind {
			case tokOpen:
				depth++
			case tokClose:
				depth = max(depth-1, 0)
			case tokSemi:
				if depth == 0 {
					return toks[k].start, k + 1
				}
			case tokSlash:
				return toks[k].start, k + 1
			}
		}
		return len(s), len(toks)
	}
	b := blockTracker{heuristic: heuristic}
	for k := j; k < len(toks); k++ {
		t := toks[k]
		if t.kind == tokSlash {
			return t.start, k + 1
		}
		if b.feed(toks, k) {
			return t.end, k + 1
		}
	}
	return len(s), len(toks)
}

// unitKind reports whether the statement starting at toks[0] is a PL/SQL
// unit, and whether its end may be found without a "/" line.
func unitKind(toks []token) (unit, heuristic bool) {
	w := codeWords(skipLabel(toks), 8)
	if len(w) == 0 {
		return false, false
	}
	switch w[0] {
	case "declare", "begin":
		return true, true
	case "with":
		if len(w) > 1 && (w[1] == "function" || w[1] == "procedure") {
			return true, false
		}
		return false, false
	case "create":
	default:
		return false, false
	}
	k := 1
	if len(w) > 2 && w[1] == "or" && w[2] == "replace" {
		k = 3
	}
	for k < len(w) && slices.Contains([]string{"editionable", "noneditionable", "editioning", "and", "resolve", "compile", "noforce", "force"}, w[k]) {
		k++
	}
	if k >= len(w) {
		return false, false
	}
	switch w[k] {
	case "procedure", "function", "package", "type", "library":
		return true, true
	case "trigger":
		return true, !slices.Contains(codeWords(toks, 64), "compound")
	case "java":
		return true, false
	}
	return false, false
}

// skipLabel drops a leading <<label>> (and comments).
func skipLabel(toks []token) []token {
	code := slices.DeleteFunc(slices.Clone(toks), func(t token) bool { return t.kind == tokComment })
	if len(code) >= 5 && code[0].low == "<" && code[1].low == "<" && code[2].kind == tokWord &&
		code[3].low == ">" && code[4].low == ">" {
		return code[5:]
	}
	return toks
}

// codeWords returns up to n leading words of toks, skipping comments and
// stopping at the first token that is not a word.
func codeWords(toks []token, n int) []string {
	var out []string
	for _, t := range toks {
		if t.kind == tokComment {
			continue
		}
		if t.kind != tokWord || len(out) == n {
			break
		}
		out = append(out, t.low)
	}
	return out
}

// blockTracker follows the block structure of a PL/SQL unit to find the
// semicolon after its final END.
type blockTracker struct {
	heuristic bool
	stack     []byte // 'd' declarations awaiting BEGIN, 'b' block, 'c' CASE
	opened    bool
	header    bool // the unit's own CREATE ... kind words have been seen
	sub       bool // a subprogram header awaits IS/AS
	done      bool // the outermost block closed; end at the next semicolon
	depth     int  // parentheses
	skip      bool // the next word closes what END just closed (END CASE)
}

// feed processes toks[k] and reports whether the unit ends with it.
func (b *blockTracker) feed(toks []token, k int) bool {
	t := toks[k]
	switch t.kind {
	case tokOpen:
		b.depth++
	case tokClose:
		b.depth = max(b.depth-1, 0)
	case tokSemi:
		if b.depth > 0 {
			return false
		}
		b.sub = false
		// A unit without a body (forward or SQL-like type declaration, call
		// specification) ends at its first semicolon.
		return b.heuristic && (b.done || !b.opened)
	case tokWord:
		b.word(toks, k)
	}
	return false
}

func (b *blockTracker) word(toks []token, k int) {
	w := toks[k].low
	if b.skip {
		b.skip = false
		return
	}
	if b.depth > 0 && w != "case" && w != "end" {
		if w == "function" || w == "procedure" {
			b.sub = true
		}
		return
	}
	switch w {
	case "procedure", "function":
		b.sub = true
	case "package", "type", "trigger", "library":
		if !b.header {
			b.header = true
			b.sub = w != "trigger"
		}
	case "is", "as":
		if !b.sub {
			return
		}
		b.sub = false
		switch nextWord(toks, k) {
		case "object", "table", "varray", "varying", "ref", "enum", "language", "external", "json", "range":
			return
		}
		b.push('d')
	case "declare":
		b.push('d')
	case "begin":
		if n := len(b.stack); n > 0 && b.stack[n-1] == 'd' {
			b.stack[n-1] = 'b'
		} else {
			b.push('b')
		}
	case "case":
		b.push('c')
	case "end":
		switch nextWord(toks, k) {
		case "if", "loop":
			return
		case "case":
			b.skip = true
		}
		if n := len(b.stack); n > 0 {
			b.stack = b.stack[:n-1]
			if n == 1 && b.opened {
				b.done = true
			}
		}
	}
}

func (b *blockTracker) push(c byte) {
	b.stack = append(b.stack, c)
	b.opened = true
}

func nextWord(toks []token, k int) string {
	for _, t := range toks[k+1:] {
		if t.kind == tokComment {
			continue
		}
		return t.low
	}
	return ""
}

// sqlplusSet lists the SET forms that are SQL, not SQL*Plus.
var sqlplusSet = []string{"transaction", "role", "constraint", "constraints"}

// sqlplusCommands are SQL*Plus commands that end at the end of their line.
var sqlplusCommands = []string{
	"prompt", "pro", "exec", "execute", "desc", "describe", "spool", "show", "sho",
	"define", "def", "undefine", "whenever", "clear", "column", "col", "ttitle",
	"btitle", "pause", "accept", "variable", "var", "print", "host", "connect",
	"conn", "disconnect", "timing", "repheader", "repfooter", "store", "get",
	"start", "run", "list", "edit", "ed", "save", "input", "append", "change",
	"password", "archive", "recover", "startup", "shutdown", "attribute", "break",
}

// isSQLPlus reports whether toks[j], the first word of a statement, starts
// a SQL*Plus line command.
func isSQLPlus(toks []token, j int) bool {
	w := toks[j].low
	if w == "set" {
		next := nextWord(toks, j)
		return next != "" && !slices.Contains(sqlplusSet, next)
	}
	return slices.Contains(sqlplusCommands, w)
}

// codeTokens returns the tokens of text without comments.
func codeTokens(text string) []token {
	toks := lex(text)
	return slices.DeleteFunc(toks, func(t token) bool { return t.kind == tokComment })
}

// keywords returns the lower-cased bare words of a statement, skipping
// comments, literals and quoted identifiers.
func keywords(text string) []string {
	var out []string
	for _, t := range lex(text) {
		if t.kind == tokWord {
			out = append(out, t.low)
		}
	}
	return out
}

// StatementAt returns the statement under a cursor byte offset. A cursor
// inside a statement (or on its terminator) picks it; a cursor in the gap
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
