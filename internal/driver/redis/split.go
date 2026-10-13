package redis

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

// Split cuts a script into one statement per command line. Blank lines and
// lines whose first non-blank characters are "#" or "//" are skipped.
// Statement.Text is the line without surrounding whitespace (or a trailing
// CR) and Offset is the byte offset of its first character in script.
func Split(script string) []driver.Statement {
	var out []driver.Statement
	off := 0
	for line := range strings.Lines(script) {
		start := off
		off += len(line)
		body := strings.TrimRight(line, "\r\n")
		lead := len(body) - len(strings.TrimLeft(body, " \t\f\v"))
		text := strings.TrimSpace(body)
		if text == "" || isComment(text) {
			continue
		}
		out = append(out, driver.Statement{Text: text, Offset: start + lead})
	}
	return out
}

func isComment(line string) bool {
	return strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//")
}

// StatementAt returns the statement under a cursor byte offset, with the
// same rules as the PostgreSQL driver: a cursor inside a command line (or
// at its end) picks it; a cursor in the gap between two commands picks the
// previous one, unless it sits on the line where the next one starts.
// Before the first command, the first one wins.
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

// syntaxError is a command line that cannot be split into arguments; pos is
// the byte offset of the offending quote.
type syntaxError struct {
	msg string
	pos int
}

func (e *syntaxError) Error() string { return e.msg }

// Fields splits a command line into arguments the way redis-cli does:
// arguments are separated by whitespace; "double quotes" understand the
// escapes \n \r \t \b \a \\ \" and \xHH; 'single quotes' are literal except
// for \'. Quotes may start in the middle of an argument (a"b c" is "ab c"),
// but a closing quote must be followed by whitespace or the end of the line.
// Unterminated quotes are an error.
func Fields(line string) ([]string, error) {
	var out []string
	i := 0
	for {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return out, nil
		}
		var cur []byte
		inq, insq, done := false, false, false
		open := 0
		for !done {
			if i >= len(line) {
				if inq || insq {
					return nil, &syntaxError{msg: "unbalanced quotes", pos: open}
				}
				break
			}
			c := line[i]
			switch {
			case inq:
				switch {
				case c == '\\' && i+3 < len(line) && line[i+1] == 'x' && isHex(line[i+2]) && isHex(line[i+3]):
					b, _ := strconv.ParseUint(line[i+2:i+4], 16, 8)
					cur = append(cur, byte(b))
					i += 3
				case c == '\\' && i+1 < len(line):
					i++
					switch line[i] {
					case 'n':
						cur = append(cur, '\n')
					case 'r':
						cur = append(cur, '\r')
					case 't':
						cur = append(cur, '\t')
					case 'b':
						cur = append(cur, '\b')
					case 'a':
						cur = append(cur, '\a')
					default:
						cur = append(cur, line[i])
					}
				case c == '"':
					if i+1 < len(line) && !isSpace(line[i+1]) {
						return nil, &syntaxError{msg: "closing quote must be followed by a space", pos: i}
					}
					done = true
				default:
					cur = append(cur, c)
				}
			case insq:
				switch {
				case c == '\\' && i+1 < len(line) && line[i+1] == '\'':
					i++
					cur = append(cur, '\'')
				case c == '\'':
					if i+1 < len(line) && !isSpace(line[i+1]) {
						return nil, &syntaxError{msg: "closing quote must be followed by a space", pos: i}
					}
					done = true
				default:
					cur = append(cur, c)
				}
			default:
				switch {
				case isSpace(c):
					done = true
				case c == '"':
					inq, open = true, i
				case c == '\'':
					insq, open = true, i
				default:
					cur = append(cur, c)
				}
			}
			i++
		}
		out = append(out, string(cur))
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// Quote renders arg so that Fields reads it back unchanged: plain arguments
// are returned as is; anything empty or containing whitespace, quotes,
// backslashes, a leading comment marker or invalid UTF-8 is double-quoted
// with escapes.
func Quote(arg string) string {
	if arg != "" && !isComment(arg) && plain(arg) {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(arg); {
		c := arg[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		default:
			if c >= 0x80 {
				if r, size := utf8.DecodeRuneInString(arg[i:]); r != utf8.RuneError || size > 1 {
					b.WriteString(arg[i : i+size])
					i += size
					continue
				}
			}
			if c < 0x20 || c >= 0x7f {
				b.WriteString(`\x`)
				b.WriteString(strconv.FormatUint(uint64(c)>>4, 16))
				b.WriteString(strconv.FormatUint(uint64(c)&0xf, 16))
			} else {
				b.WriteByte(c)
			}
		}
		i++
	}
	b.WriteByte('"')
	return b.String()
}

func plain(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c == '"' || c == '\'' || c == '\\' || c == 0x7f {
			return false
		}
	}
	return true
}

// command renders args as one command line.
func command(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}
