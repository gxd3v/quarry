package importcsv

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Encodings reported by Sniff and accepted by Options.Encoding.
const (
	UTF8        = "UTF-8"
	UTF8BOM     = "UTF-8 BOM"
	UTF16LE     = "UTF-16LE"
	UTF16BE     = "UTF-16BE"
	Windows1252 = "Windows-1252"
)

// Preview is what Sniff found in the first rows of a CSV file.
type Preview struct {
	Header    []string // first row, or column1..columnN when there is none
	Rows      [][]string
	Delimiter rune
	HasHeader bool
	Encoding  string
}

const (
	previewRows = 20
	sampleSize  = 64 << 10
)

var errEmpty = errors.New("importcsv: empty input")

// Sniff detects the delimiter (, ; tab |), whether the first row is a header,
// and the encoding (UTF-8 with or without BOM, UTF-16 with BOM, otherwise
// Windows-1252 when the text is not valid UTF-8). It returns up to 20 data
// rows.
func Sniff(r io.Reader) (Preview, error) {
	dec, enc, err := decode(r, "")
	if err != nil {
		return Preview{}, err
	}
	buf := make([]byte, sampleSize)
	n, err := io.ReadFull(dec, buf)
	truncated := err == nil
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Preview{}, err
	}
	sample := buf[:n]
	if enc == UTF8 && !validPrefix(sample, truncated) {
		enc = Windows1252
		sample = []byte(decode1252(sample))
	}
	if truncated {
		// Drop the partial last line; a quoted field may still be cut, so
		// a parse error at the end of the sample is tolerated below.
		if i := bytes.LastIndexByte(sample, '\n'); i >= 0 {
			sample = sample[:i+1]
		}
	}
	if len(bytes.TrimSpace(sample)) == 0 {
		return Preview{Encoding: enc}, errEmpty
	}

	p := Preview{Delimiter: ',', Encoding: enc}
	var records [][]string
	best := -1
	for _, d := range []rune{',', ';', '\t', '|'} {
		recs := parseSample(sample, d)
		if s := score(recs); s > best {
			best, p.Delimiter, records = s, d, recs
		}
	}
	if len(records) == 0 {
		return p, errEmpty
	}
	p.HasHeader = looksLikeHeader(records)
	if p.HasHeader {
		p.Header, records = records[0], records[1:]
	} else {
		width := 0
		for _, r := range records {
			width = max(width, len(r))
		}
		p.Header = make([]string, width)
		for i := range p.Header {
			p.Header[i] = "column" + strconv.Itoa(i+1)
		}
	}
	p.Rows = records[:min(len(records), previewRows)]
	return p, nil
}

// parseSample reads up to previewRows+1 records, stopping at the first error.
func parseSample(sample []byte, delim rune) [][]string {
	cr := newCSVReader(bytes.NewReader(sample), delim)
	cr.ReuseRecord = false
	var out [][]string
	for len(out) <= previewRows {
		rec, err := cr.Read()
		if err != nil {
			break
		}
		out = append(out, rec)
	}
	return out
}

// score rates how well a delimiter splits the records: the number of
// records sharing the most common field count, weighted by that count.
// Delimiters that never split a line score 0.
func score(recs [][]string) int {
	counts := map[int]int{}
	mode := 0
	for _, r := range recs {
		counts[len(r)]++
		if counts[len(r)] > counts[mode] || counts[len(r)] == counts[mode] && len(r) > mode {
			mode = len(r)
		}
	}
	if mode < 2 {
		return 0
	}
	return counts[mode]*1000 - (len(recs)-counts[mode])*1000 + mode
}

// looksLikeHeader guesses whether the first record names the columns: its
// cells are non-empty, unique and not numbers or dates, and no column
// contradicts it (typed data under it, or its own value repeated below).
func looksLikeHeader(recs [][]string) bool {
	first := recs[0]
	seen := map[string]bool{}
	for _, c := range first {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] || kindOf(c) != kindText {
			return false
		}
		seen[c] = true
	}
	if len(recs) == 1 {
		for _, c := range first {
			if !identLike(c) {
				return false
			}
		}
		return true
	}
	votes := 0
	for col, h := range first {
		kind, uniform := kindNone, true
		for _, r := range recs[1:] {
			if col >= len(r) || strings.TrimSpace(r[col]) == "" {
				continue
			}
			if r[col] == h {
				votes--
			}
			switch k := kindOf(strings.TrimSpace(r[col])); kind {
			case kindNone:
				kind = k
			case k:
			default:
				uniform = false
			}
		}
		if uniform && kind > kindText {
			votes++
		}
	}
	return votes >= 0
}

type cellKind int

const (
	kindNone cellKind = iota - 1
	kindText
	kindNumber
	kindDate
)

func kindOf(s string) cellKind {
	if _, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64); err == nil {
		return kindNumber
	}
	if len(s) >= 8 && strings.Count(s, "-")+strings.Count(s, "/") >= 2 &&
		strings.IndexFunc(s, unicode.IsLetter) < 0 && strings.IndexFunc(s, unicode.IsDigit) == 0 {
		return kindDate
	}
	return kindText
}

func identLike(s string) bool {
	for i, r := range s {
		if !(unicode.IsLetter(r) || r == '_' || i > 0 && (unicode.IsDigit(r) || r == ' ' || r == '-' || r == '.')) {
			return false
		}
	}
	return s != ""
}

func newCSVReader(r io.Reader, delim rune) *csv.Reader {
	cr := csv.NewReader(r)
	if delim == 0 {
		delim = ','
	}
	cr.Comma = delim
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	return cr
}

// validPrefix reports whether b is valid UTF-8, allowing an incomplete
// rune at the end when the sample was cut.
func validPrefix(b []byte, truncated bool) bool {
	if truncated {
		for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
			if utf8.RuneStart(b[i]) {
				if !utf8.FullRune(b[i:]) {
					b = b[:i]
				}
				break
			}
		}
	}
	return utf8.Valid(b)
}

// decode returns r as UTF-8 text without a BOM. enc forces an encoding;
// "" detects a BOM and otherwise assumes UTF-8.
func decode(r io.Reader, enc string) (io.Reader, string, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(3)
	if err != nil && err != io.EOF {
		return nil, "", err
	}
	switch {
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		br.Discard(3)
		return br, UTF8BOM, nil
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}):
		br.Discard(2)
		return &utf16Reader{r: br}, UTF16LE, nil
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		br.Discard(2)
		return &utf16Reader{r: br, be: true}, UTF16BE, nil
	case strings.EqualFold(enc, Windows1252):
		return &cp1252Reader{r: br}, Windows1252, nil
	case strings.EqualFold(enc, UTF16LE):
		return &utf16Reader{r: br}, UTF16LE, nil
	case strings.EqualFold(enc, UTF16BE):
		return &utf16Reader{r: br, be: true}, UTF16BE, nil
	}
	return br, UTF8, nil
}

type utf16Reader struct {
	r   *bufio.Reader
	be  bool
	out []byte
}

func (u *utf16Reader) unit() (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(u.r, b[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return 0, err
	}
	if u.be {
		return uint16(b[0])<<8 | uint16(b[1]), nil
	}
	return uint16(b[1])<<8 | uint16(b[0]), nil
}

func (u *utf16Reader) Read(p []byte) (int, error) {
	for len(u.out) < len(p) && len(u.out) < 4096 {
		c, err := u.unit()
		if err != nil {
			if len(u.out) > 0 {
				break
			}
			return 0, err
		}
		r := rune(c)
		if utf16.IsSurrogate(r) {
			c2, err := u.unit()
			if err != nil {
				r = utf8.RuneError
			} else {
				r = utf16.DecodeRune(r, rune(c2))
			}
		}
		u.out = utf8.AppendRune(u.out, r)
	}
	n := copy(p, u.out)
	u.out = u.out[:copy(u.out, u.out[n:])]
	return n, nil
}

// cp1252 maps bytes 0x80..0x9F; the rest of Windows-1252 equals Latin-1.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func decode1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

type cp1252Reader struct {
	r   io.Reader
	in  []byte
	out []byte
}

func (c *cp1252Reader) Read(p []byte) (int, error) {
	if len(c.out) == 0 {
		if c.in == nil {
			c.in = make([]byte, 4096)
		}
		n, err := c.r.Read(c.in)
		if n == 0 {
			return 0, err
		}
		c.out = append(c.out[:0], decode1252(c.in[:n])...)
	}
	n := copy(p, c.out)
	c.out = c.out[:copy(c.out, c.out[n:])]
	return n, nil
}
