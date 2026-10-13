// Package params finds bind parameters in SQL scripts and substitutes
// user-supplied values as literals, so a script with :name or $1 placeholders
// can run after the user fills them in.
package params

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

// Param is one distinct parameter. Name excludes the sigil: ":id" and "$1"
// are named "id" and "1" (so Oracle's :1 and PostgreSQL's $1 share a name).
// Positions are the byte offsets of the sigil of each occurrence.
type Param struct {
	Name      string
	Positions []int
}

// Find lists the parameters of a SQL statement in order of first occurrence:
// :name and :1 (Oracle, named binds) and $1 (PostgreSQL positional). Text in
// strings, quoted identifiers, comments and dollar quotes is ignored, as are
// ::casts, := assignments, array slices like a[1:n] and :new.col / :old.col
// trigger references. Mongo and Redis have no parameters.
func Find(lang driver.Language, stmt string) []Param {
	if lang != driver.LangSQL {
		return nil
	}
	var (
		out      []Param
		seen     = map[string]int{}
		brackets int
		s        = stmt
	)
	add := func(name string, pos int) {
		if i, ok := seen[name]; ok {
			out[i].Positions = append(out[i].Positions, pos)
			return
		}
		seen[name] = len(out)
		out = append(out, Param{Name: name, Positions: []int{pos}})
	}
	for i := 0; i < len(s); {
		c := s[i]
		var peek byte
		if i+1 < len(s) {
			peek = s[i+1]
		}
		switch {
		case c == '-' && peek == '-':
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(s)
			}
		case c == '/' && peek == '*':
			i = skipBlockComment(s, i)
		case c == '\'' || c == '"':
			i = skipString(s, i+1, false)
		case c == '$':
			if tag, ok := dollarTag(s, i); ok {
				if k := strings.Index(s[i+len(tag):], tag); k >= 0 {
					i += 2*len(tag) + k
				} else {
					i = len(s)
				}
				continue
			}
			j := i + 1
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			if j > i+1 {
				add(s[i+1:j], i)
			}
			i = j
		case c == ':':
			switch {
			case peek == ':' || peek == '=':
				i += 2
			case isNameChar(peek):
				j := i + 1
				for j < len(s) && isNameChar(s[j]) {
					j++
				}
				record := j+1 < len(s) && s[j] == '.' && isIdentStart(s[j+1])
				if brackets == 0 && !record {
					add(s[i+1:j], i)
				}
				i = j
			default:
				i++
			}
		case c == '[':
			brackets++
			i++
		case c == ']':
			brackets = max(brackets-1, 0)
			i++
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			if j < len(s) && s[j] == '\'' {
				switch strings.ToLower(s[i:j]) {
				case "e":
					i = skipString(s, j+1, true)
					continue
				case "q", "nq":
					i = skipQQuote(s, j)
					continue
				}
			}
			i = j
		case isDigit(c):
			j := i + 1
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			i = j
		default:
			i++
		}
	}
	return out
}

// Substitute replaces every parameter of stmt with values[name] rendered as
// a literal: NULL for exactly "NULL" (any case), a bare number for numeric
// text (surrounding spaces ignored; leading zeros, as in "007", keep it a
// string), and a single-quoted string with quotes doubled otherwise. A value
// the user wrapped in single quotes is always a string, and doubled quotes
// inside it are unescaped first. dialect is the driver kind ("postgres",
// "oracle"); PostgreSQL rejects NUL bytes in strings. It fails, naming every
// missing parameter, when some parameter has no value.
func Substitute(lang driver.Language, stmt string, values map[string]string, dialect string) (string, error) {
	type occ struct {
		pos, end int
		lit      string
	}
	var (
		occs    []occ
		missing []string
	)
	for _, p := range Find(lang, stmt) {
		label := stmt[p.Positions[0]:p.Positions[0]+1] + p.Name
		v, ok := values[p.Name]
		if !ok {
			missing = append(missing, label)
			continue
		}
		lit, err := literal(v, dialect)
		if err != nil {
			return "", fmt.Errorf("parameter %s: %w", label, err)
		}
		for _, pos := range p.Positions {
			occs = append(occs, occ{pos, pos + 1 + len(p.Name), lit})
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("no value for parameter %s", strings.Join(missing, ", "))
	}
	if len(occs) == 0 {
		return stmt, nil
	}
	slices.SortFunc(occs, func(a, b occ) int { return a.pos - b.pos })
	var b strings.Builder
	last := 0
	for _, o := range occs {
		b.WriteString(stmt[last:o.pos])
		b.WriteString(o.lit)
		last = o.end
	}
	b.WriteString(stmt[last:])
	return b.String(), nil
}

func literal(v, dialect string) (string, error) {
	t := strings.TrimSpace(v)
	switch {
	case strings.EqualFold(v, "null"):
		return "NULL", nil
	case len(t) >= 2 && t[0] == '\'' && t[len(t)-1] == '\'':
		return quote(strings.ReplaceAll(t[1:len(t)-1], "''", "'"), dialect)
	case isNumber(t):
		t = strings.TrimPrefix(t, "+")
		if t[0] == '-' {
			return "(" + t + ")", nil // x-:p must not become x--5, a comment
		}
		return t, nil
	}
	return quote(v, dialect)
}

func quote(s, dialect string) (string, error) {
	if dialect == string(driver.Postgres) && strings.IndexByte(s, 0) >= 0 {
		return "", fmt.Errorf("PostgreSQL strings cannot contain NUL bytes")
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

// isNumber accepts [+-]digits[.digits][e[+-]digits] (or .5), without
// superfluous leading zeros.
func isNumber(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	intStart := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	intLen := i - intStart
	if intLen > 1 && s[intStart] == '0' {
		return false
	}
	fracLen := 0
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
			fracLen++
		}
	}
	if intLen == 0 && fracLen == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		expStart := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == expStart {
			return false
		}
	}
	return i == len(s)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || c == '$' || isDigit(c) }

// isNameChar is a bind-name character: like an identifier, without '$'.
func isNameChar(c byte) bool { return isIdentStart(c) || isDigit(c) }

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
