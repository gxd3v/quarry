package mongo

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokKind classifies mongosh tokens.
type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tString // '…', "…" and `…` (no interpolation)
	tNumber
	tRegex
	tPunct
	tComment
)

func (k tokKind) String() string {
	switch k {
	case tEOF:
		return "end of statement"
	case tIdent:
		return "identifier"
	case tString:
		return "string"
	case tNumber:
		return "number"
	case tRegex:
		return "regular expression"
	case tPunct:
		return "punctuation"
	case tComment:
		return "comment"
	}
	return "token"
}

// token is one lexeme of a statement. start/end are byte offsets into the
// lexed text.
type token struct {
	kind       tokKind
	start, end int
	text       string // raw source text
	val        string // strings: decoded value; regex: pattern
	flags      string // regex flags
}

func (t token) is(kind tokKind, text string) bool { return t.kind == kind && t.text == text }
func (t token) punct(p string) bool                { return t.kind == tPunct && t.text == p }

// lexError is a tokenizer failure at a byte offset.
type lexError struct {
	pos int
	msg string
}

func (e *lexError) Error() string { return e.msg }

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// space reports the size of the whitespace rune at s[i], 0 if none, and
// whether it is a line terminator.
func space(s string, i int) (size int, newline bool) {
	c := s[i]
	if c < utf8.RuneSelf {
		if isSpaceByte(c) {
			return 1, c == '\n' || c == '\r'
		}
		return 0, false
	}
	r, n := utf8.DecodeRuneInString(s[i:])
	switch r {
	case ' ', ' ':
		return n, true
	case ' ', '﻿':
		return n, false
	}
	if unicode.IsSpace(r) {
		return n, false
	}
	return 0, false
}

func isIdentStartRune(r rune) bool {
	return r == '_' || r == '$' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') ||
		(r >= utf8.RuneSelf && unicode.IsLetter(r))
}

func isIdentPartRune(r rune) bool {
	return isIdentStartRune(r) || ('0' <= r && r <= '9') ||
		(r >= utf8.RuneSelf && (unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)))
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// regexAllowedAfter reports whether a '/' following prev starts a regular
// expression literal rather than a division.
func regexAllowedAfter(prev token, hasPrev bool) bool {
	if !hasPrev {
		return true
	}
	switch prev.kind {
	case tPunct:
		return strings.Contains("(,=:[!&|?{};+-*%<>~^", prev.text)
	case tIdent:
		switch prev.text {
		case "return", "typeof", "case", "do", "else", "in", "of", "new", "delete", "void", "throw":
			return true
		}
	}
	return false
}

// scan reads the token starting at s[i] (which must not be whitespace). It
// never fails: problems are reported in errMsg while end still advances, so
// tolerant callers (Split) can keep going.
func scan(s string, i int, regexOK bool) (kind tokKind, end int, errMsg string) {
	c := s[i]
	var peek byte
	if i+1 < len(s) {
		peek = s[i+1]
	}
	switch {
	case c == '/' && peek == '/':
		j := i + 2
		for j < len(s) {
			if n, nl := space(s, j); nl {
				break
			} else if n > 0 {
				j += n
				continue
			}
			j++
		}
		return tComment, j, ""
	case c == '/' && peek == '*':
		if k := strings.Index(s[i+2:], "*/"); k >= 0 {
			return tComment, i + 2 + k + 2, ""
		}
		return tComment, len(s), "unterminated comment"
	case c == '\'' || c == '"' || c == '`':
		j := i + 1
		for j < len(s) {
			switch s[j] {
			case '\\':
				j += 2
				continue
			case c:
				return tString, j + 1, ""
			case '\n', '\r':
				if c != '`' {
					return tString, j, "unterminated string"
				}
			}
			j++
		}
		return tString, len(s), "unterminated string"
	case c == '/' && regexOK:
		j := i + 1
		class := false
		for j < len(s) {
			switch s[j] {
			case '\\':
				j += 2
				continue
			case '[':
				class = true
			case ']':
				class = false
			case '/':
				if !class {
					j++
					for j < len(s) && s[j] < utf8.RuneSelf && isIdentPartRune(rune(s[j])) {
						j++
					}
					return tRegex, j, ""
				}
			case '\n', '\r':
				return tRegex, j, "unterminated regular expression"
			}
			j++
		}
		return tRegex, len(s), "unterminated regular expression"
	case isDigit(c) || (c == '.' && isDigit(peek)):
		return scanNumber(s, i)
	}
	r, n := utf8.DecodeRuneInString(s[i:])
	if isIdentStartRune(r) || r == '\\' {
		j := i + n
		for j < len(s) {
			r, n := utf8.DecodeRuneInString(s[j:])
			if !isIdentPartRune(r) {
				break
			}
			j += n
		}
		if r == '\\' {
			return tIdent, j, "unicode escapes in identifiers are not supported"
		}
		return tIdent, j, ""
	}
	if c == '.' && strings.HasPrefix(s[i:], "...") {
		return tPunct, i + 3, ""
	}
	if c >= utf8.RuneSelf {
		return tPunct, i + n, "unexpected character " + strconv.QuoteRune(r)
	}
	return tPunct, i + 1, ""
}

func scanNumber(s string, i int) (tokKind, int, string) {
	j := i
	digits := func(ok func(byte) bool) {
		for j < len(s) && (ok(s[j]) || s[j] == '_') {
			j++
		}
	}
	if s[j] == '0' && j+1 < len(s) && strings.ContainsRune("xXoObB", rune(s[j+1])) {
		j += 2
		digits(func(b byte) bool { return isDigit(b) || ('a' <= b && b <= 'f') || ('A' <= b && b <= 'F') })
	} else {
		digits(isDigit)
		if j < len(s) && s[j] == '.' {
			j++
			digits(isDigit)
		}
		if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
			k := j + 1
			if k < len(s) && (s[k] == '+' || s[k] == '-') {
				k++
			}
			if k < len(s) && isDigit(s[k]) {
				j = k
				digits(isDigit)
			}
		}
	}
	if j < len(s) && s[j] == 'n' { // BigInt suffix
		j++
	}
	if j < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[j:]); isIdentPartRune(r) {
			k := j
			for k < len(s) {
				r, n := utf8.DecodeRuneInString(s[k:])
				if !isIdentPartRune(r) {
					break
				}
				k += n
			}
			return tNumber, k, "invalid number " + strconv.Quote(s[i:k])
		}
	}
	return tNumber, j, ""
}

// lex tokenizes one statement strictly: comments are dropped and the first
// malformed token is an error.
func lex(s string) ([]token, error) {
	var out []token
	i := 0
	for i < len(s) {
		if n, _ := space(s, i); n > 0 {
			i += n
			continue
		}
		var prev token
		if len(out) > 0 {
			prev = out[len(out)-1]
		}
		kind, end, msg := scan(s, i, regexAllowedAfter(prev, len(out) > 0))
		if msg != "" {
			return nil, &lexError{pos: i, msg: msg}
		}
		t := token{kind: kind, start: i, end: end, text: s[i:end]}
		switch kind {
		case tComment:
			i = end
			continue
		case tString:
			v, err := unquote(s, i, end)
			if err != nil {
				return nil, err
			}
			t.val = v
		case tRegex:
			k := strings.LastIndexByte(t.text, '/')
			t.val, t.flags = t.text[1:k], t.text[k+1:]
		}
		out = append(out, t)
		i = end
	}
	return out, nil
}

// unquote decodes the JS string literal s[start:end] (quotes included).
func unquote(s string, start, end int) (string, error) {
	q := s[start]
	body := s[start+1 : end-1]
	if q == '`' {
		if k := strings.Index(body, "${"); k >= 0 && (k == 0 || body[k-1] != '\\') {
			return "", &lexError{pos: start + 1 + k, msg: "template literal interpolation is not supported"}
		}
	}
	if !strings.Contains(body, `\`) {
		return body, nil
	}
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		pos := start + 1 + i
		if i+1 >= len(body) {
			return "", &lexError{pos: pos, msg: "invalid escape sequence"}
		}
		e := body[i+1]
		i += 2
		switch e {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '0':
			if i < len(body) && isDigit(body[i]) {
				return "", &lexError{pos: pos, msg: "octal escape sequences are not supported"}
			}
			b.WriteByte(0)
		case '\r':
			if i < len(body) && body[i] == '\n' {
				i++
			}
		case '\n':
			// line continuation
		case 'x':
			if i+2 > len(body) {
				return "", &lexError{pos: pos, msg: `invalid \x escape`}
			}
			n, err := strconv.ParseUint(body[i:i+2], 16, 8)
			if err != nil {
				return "", &lexError{pos: pos, msg: `invalid \x escape`}
			}
			b.WriteRune(rune(n))
			i += 2
		case 'u':
			var hexs string
			if i < len(body) && body[i] == '{' {
				k := strings.IndexByte(body[i:], '}')
				if k < 0 {
					return "", &lexError{pos: pos, msg: `invalid \u escape`}
				}
				hexs = body[i+1 : i+k]
				i += k + 1
			} else {
				if i+4 > len(body) {
					return "", &lexError{pos: pos, msg: `invalid \u escape`}
				}
				hexs = body[i : i+4]
				i += 4
			}
			n, err := strconv.ParseUint(hexs, 16, 32)
			if err != nil || hexs == "" || n > unicode.MaxRune {
				return "", &lexError{pos: pos, msg: `invalid \u escape`}
			}
			r := rune(n)
			// Combine a UTF-16 surrogate pair.
			if 0xD800 <= r && r < 0xDC00 && i+6 <= len(body) && body[i] == '\\' && body[i+1] == 'u' {
				if lo, err := strconv.ParseUint(body[i+2:i+6], 16, 32); err == nil && 0xDC00 <= lo && lo < 0xE000 {
					r = 0x10000 + (r-0xD800)<<10 + (rune(lo) - 0xDC00)
					i += 6
				}
			}
			b.WriteRune(r)
		default:
			b.WriteByte(e) // \' \" \\ \` \/ and identity escapes
		}
	}
	return b.String(), nil
}
