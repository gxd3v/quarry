package export

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/gxd3v/quarry/internal/driver"
)

type formatter interface {
	page(rows [][]any) error
	finish() error
}

func newFormatter(w *bufio.Writer, cols []driver.Column, o Options) formatter {
	eol := "\n"
	if o.CRLF {
		eol = "\r\n"
	}
	switch o.Format {
	case JSON, NDJSON:
		return newJSONWriter(w, cols, o, eol)
	case SQLInsert:
		return newSQLWriter(w, cols, o, eol)
	case Markdown:
		return &mdWriter{w: w, cols: cols, header: o.Header, eol: eol}
	}
	return &csvWriter{w: w, cols: cols, o: o, eol: eol}
}

// cell returns row[c], or nil when the row is short.
func cell(row []any, c int) any {
	if c < len(row) {
		return row[c]
	}
	return nil
}

// csvWriter writes CSV and TSV. Fields are quoted RFC 4180 style when they
// contain the delimiter, a quote or a line break, and also when they equal
// NullText so NULL stays distinguishable (as PostgreSQL's COPY reads it).
type csvWriter struct {
	w       *bufio.Writer
	cols    []driver.Column
	o       Options
	eol     string
	started bool
	buf     []byte
}

func (c *csvWriter) start() {
	c.started = true
	if !c.o.Header {
		return
	}
	for i, col := range c.cols {
		if i > 0 {
			c.w.WriteRune(c.o.Delimiter)
		}
		c.field([]byte(col.Name), c.o.QuoteAll && c.o.Format == CSV)
	}
	c.w.WriteString(c.eol)
}

func (c *csvWriter) page(rows [][]any) error {
	if !c.started {
		c.start()
	}
	for _, row := range rows {
		for i := range c.cols {
			if i > 0 {
				c.w.WriteRune(c.o.Delimiter)
			}
			v := cell(row, i)
			if v == nil {
				c.field([]byte(c.o.NullText), false)
				continue
			}
			c.buf = AppendText(c.buf[:0], v)
			c.field(c.buf, c.o.QuoteAll && c.o.Format == CSV || string(c.buf) == c.o.NullText)
		}
		c.w.WriteString(c.eol)
	}
	return nil
}

func (c *csvWriter) field(b []byte, quote bool) {
	if !quote && !c.special(b) {
		c.w.Write(b)
		return
	}
	c.w.WriteByte('"')
	for {
		i := bytes.IndexByte(b, '"')
		if i < 0 {
			break
		}
		c.w.Write(b[:i+1])
		c.w.WriteByte('"')
		b = b[i+1:]
	}
	c.w.Write(b)
	c.w.WriteByte('"')
}

func (c *csvWriter) special(b []byte) bool {
	d := c.o.Delimiter
	if d >= utf8.RuneSelf {
		if bytes.ContainsRune(b, d) {
			return true
		}
		d = '"'
	}
	for _, ch := range b {
		if ch == '"' || ch == '\n' || ch == '\r' || rune(ch) == d {
			return true
		}
	}
	return false
}

func (c *csvWriter) finish() error {
	if !c.started {
		c.start()
	}
	return nil
}

// jsonWriter writes a JSON array with one object per line, or NDJSON. Each
// row is a top-level value for the encoder, which flushes it whole to
// rowSink; the framing between rows is written directly.
type jsonWriter struct {
	w       *bufio.Writer
	enc     *jsontext.Encoder
	keys    []string
	array   bool
	eol     string
	rows    int64
	scratch []byte
}

// rowSink drops the newline the encoder appends after each top-level value;
// compact JSON has no other raw newlines.
type rowSink struct{ w *bufio.Writer }

func (s rowSink) Write(p []byte) (int, error) {
	n := len(p)
	if n > 0 && p[n-1] == '\n' {
		p = p[:n-1]
	}
	_, err := s.w.Write(p)
	return n, err
}

func newJSONWriter(w *bufio.Writer, cols []driver.Column, o Options, eol string) *jsonWriter {
	return &jsonWriter{
		w:     w,
		enc:   jsontext.NewEncoder(rowSink{w}, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)),
		keys:  UniqueNames(cols),
		array: o.Format == JSON,
		eol:   eol,
	}
}

func (j *jsonWriter) page(rows [][]any) error {
	for _, row := range rows {
		switch {
		case !j.array:
		case j.rows == 0:
			j.w.WriteByte('[')
			j.w.WriteString(j.eol)
		default:
			j.w.WriteByte(',')
			j.w.WriteString(j.eol)
		}
		j.rows++
		if err := j.enc.WriteToken(jsontext.BeginObject); err != nil {
			return err
		}
		for i, k := range j.keys {
			if err := j.enc.WriteToken(jsontext.String(k)); err != nil {
				return err
			}
			if err := encodeJSON(j.enc, cell(row, i), &j.scratch); err != nil {
				return err
			}
		}
		if err := j.enc.WriteToken(jsontext.EndObject); err != nil {
			return err
		}
		if !j.array {
			j.w.WriteString(j.eol)
		}
	}
	return nil
}

func (j *jsonWriter) finish() error {
	switch {
	case !j.array:
	case j.rows == 0:
		j.w.WriteString("[]" + j.eol)
	default:
		j.w.WriteString(j.eol + "]" + j.eol)
	}
	return nil
}

// sqlWriter writes INSERT statements, BatchRows rows per statement: a
// multi-row VALUES list, or INSERT ALL for Oracle.
type sqlWriter struct {
	w       *bufio.Writer
	ncols   int
	into    string // INSERT INTO t (a, b)
	dialect string
	oracle  bool
	batch   int
	eol     string
	n       int // rows in the open statement
	buf     []byte
}

func newSQLWriter(w *bufio.Writer, cols []driver.Column, o Options, eol string) *sqlWriter {
	var sb strings.Builder
	sb.WriteString("INSERT INTO " + o.Table + " (")
	for i, c := range cols {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(QuoteIdent(c.Name, o.Dialect))
	}
	sb.WriteByte(')')
	return &sqlWriter{w: w, ncols: len(cols), into: sb.String(), dialect: o.Dialect,
		oracle: isOracle(o.Dialect), batch: o.BatchRows, eol: eol}
}

func (s *sqlWriter) page(rows [][]any) error {
	for _, row := range rows {
		b := s.buf[:0]
		switch {
		case s.batch == 1:
			b = append(b, s.into...)
			b = append(b, " VALUES ("...)
		case s.oracle:
			if s.n == 0 {
				b = append(b, "INSERT ALL"...)
				b = append(b, s.eol...)
			}
			b = append(b, "  "...)
			b = append(b, s.into[len("INSERT "):]...)
			b = append(b, " VALUES ("...)
		case s.n == 0:
			b = append(b, s.into...)
			b = append(b, " VALUES"...)
			b = append(b, s.eol...)
			b = append(b, "  ("...)
		default:
			b = append(b, ","...)
			b = append(b, s.eol...)
			b = append(b, "  ("...)
		}
		for i := range s.ncols {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = AppendSQLLiteral(b, cell(row, i), s.dialect)
		}
		b = append(b, ')')
		switch {
		case s.batch == 1:
			b = append(b, ';')
			b = append(b, s.eol...)
		case s.oracle:
			b = append(b, s.eol...)
		}
		s.buf = b
		s.w.Write(b)
		if s.n++; s.n == s.batch {
			s.closeBatch()
		}
	}
	return nil
}

func (s *sqlWriter) closeBatch() {
	switch {
	case s.batch == 1:
	case s.oracle:
		s.w.WriteString("SELECT 1 FROM DUAL;" + s.eol)
	default:
		s.w.WriteString(";" + s.eol)
	}
	s.n = 0
}

func (s *sqlWriter) finish() error {
	if s.n > 0 {
		s.closeBatch()
	}
	return nil
}

// mdWriter writes a GitHub-flavoured pipe table. Column widths and numeric
// right-alignment come from the header and the first page; later, wider
// cells simply overflow their column, which Markdown tolerates.
type mdWriter struct {
	w       *bufio.Writer
	cols    []driver.Column
	header  bool
	eol     string
	started bool
	widths  []int
	right   []bool
	buf     []byte
}

var mdReplacer = strings.NewReplacer(`\`, `\\`, "|", `\|`, "\r\n", "<br>", "\n", "<br>", "\r", "<br>", "\t", " ")

// appendMD appends v as Markdown cell text: NULL, or Text(v) with pipes,
// backslashes and line breaks escaped.
func appendMD(dst []byte, v any) []byte {
	if v == nil {
		return append(dst, "NULL"...)
	}
	mark := len(dst)
	dst = AppendText(dst, v)
	if bytes.ContainsAny(dst[mark:], "\\|\r\n\t") {
		s := mdReplacer.Replace(string(dst[mark:]))
		dst = append(dst[:mark], s...)
	}
	return dst
}

func (m *mdWriter) start(first [][][]byte, sample [][]any) {
	m.started = true
	n := len(m.cols)
	m.widths = make([]int, n)
	m.right = make([]bool, n)
	head := make([][]byte, n)
	for j, c := range m.cols {
		if m.header {
			head[j] = []byte(mdReplacer.Replace(c.Name))
		}
		numeric, some := true, false
		for _, row := range sample {
			switch cell(row, j).(type) {
			case nil:
			case int64, float64, driver.Number:
				some = true
			default:
				numeric = false
			}
		}
		m.right[j] = numeric && some
		m.widths[j] = max(textWidth(head[j]), 3)
		for _, r := range first {
			m.widths[j] = max(m.widths[j], textWidth(r[j]))
		}
	}
	m.line(head)
	m.w.WriteByte('|')
	for j, w := range m.widths {
		m.w.WriteByte(' ')
		if m.right[j] {
			m.w.WriteString(strings.Repeat("-", w-1) + ":")
		} else {
			m.w.WriteString(strings.Repeat("-", w))
		}
		m.w.WriteString(" |")
	}
	m.w.WriteString(m.eol)
}

func (m *mdWriter) line(cells [][]byte) {
	m.w.WriteByte('|')
	for j, v := range cells {
		m.cell(j, v)
	}
	m.w.WriteString(m.eol)
}

// cell writes " v |" padded to column j's width.
func (m *mdWriter) cell(j int, v []byte) {
	pad := max(m.widths[j]-textWidth(v), 0)
	m.w.WriteByte(' ')
	if m.right[j] {
		m.pad(pad)
	}
	m.w.Write(v)
	if !m.right[j] {
		m.pad(pad)
	}
	m.w.WriteString(" |")
}

func (m *mdWriter) pad(n int) {
	for range n {
		m.w.WriteByte(' ')
	}
}

func (m *mdWriter) page(rows [][]any) error {
	if !m.started {
		first := make([][][]byte, len(rows))
		for i, row := range rows {
			first[i] = make([][]byte, len(m.cols))
			for j := range m.cols {
				first[i][j] = appendMD(nil, cell(row, j))
			}
		}
		m.start(first, rows)
		for _, r := range first {
			m.line(r)
		}
		return nil
	}
	for _, row := range rows {
		m.w.WriteByte('|')
		for j := range m.cols {
			m.buf = appendMD(m.buf[:0], cell(row, j))
			m.cell(j, m.buf)
		}
		m.w.WriteString(m.eol)
	}
	return nil
}

func (m *mdWriter) finish() error {
	if !m.started {
		m.start(nil, nil)
	}
	return nil
}

// textWidth is ansi.StringWidth with an ASCII fast path.
func textWidth(b []byte) int {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return ansi.StringWidth(string(b))
		}
	}
	return len(b)
}
