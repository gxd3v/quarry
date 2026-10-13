package query

import (
	"strings"
	"unicode"
	"unicode/utf8"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/postgres"
)

// splitter cuts a script into statements in a backend's dialect.
type splitter struct {
	split func(script string) []driver.Statement
	at    func(stmts []driver.Statement, script string, offset int) (driver.Statement, bool)
}

var splitters = map[driver.Kind]splitter{
	driver.Postgres: {split: postgres.Split, at: postgres.StatementAt},
}

func splitterFor(k driver.Kind) splitter {
	if sp, ok := splitters[k]; ok {
		return sp
	}
	return splitter{split: whole, at: first}
}

// whole treats the trimmed script as a single statement.
func whole(script string) []driver.Statement {
	start := len(script) - len(strings.TrimLeftFunc(script, unicode.IsSpace))
	text := strings.TrimRightFunc(script[start:], unicode.IsSpace)
	if text == "" {
		return nil
	}
	return []driver.Statement{{Text: text, Offset: start}}
}

func first(stmts []driver.Statement, _ string, _ int) (driver.Statement, bool) {
	if len(stmts) == 0 {
		return driver.Statement{}, false
	}
	return stmts[0], true
}

func indexOf(stmts []driver.Statement, st driver.Statement) int {
	for i, s := range stmts {
		if s.Offset == st.Offset {
			return i
		}
	}
	return -1
}

// inRange returns the statements intersecting the byte range [start, end).
func inRange(stmts []driver.Statement, start, end int) []driver.Statement {
	var out []driver.Statement
	for _, st := range stmts {
		if st.Offset < end && st.Offset+len(st.Text) > start {
			out = append(out, st)
		}
	}
	return out
}

// utf16Conv converts ascending byte offsets of s to UTF-16 offsets in one
// pass; going backwards restarts from the beginning.
type utf16Conv struct {
	s    string
	b, u int
}

func (c *utf16Conv) at(b int) int {
	b = min(max(b, 0), len(c.s))
	if b < c.b {
		c.b, c.u = 0, 0
	}
	for c.b < b {
		r, size := utf8.DecodeRuneInString(c.s[c.b:])
		c.b += size
		c.u++
		if r >= 0x10000 {
			c.u++
		}
	}
	return c.u
}

func rangeOf(c *utf16Conv, st driver.Statement) *quarryv1.StatementRange {
	return &quarryv1.StatementRange{Start: int32(c.at(st.Offset)), End: int32(c.at(st.Offset + len(st.Text)))}
}
