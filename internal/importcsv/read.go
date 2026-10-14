// Package importcsv reads CSV files for importing into tables: Sniff guesses
// the format from a sample, Reader streams the records and AutoMap matches
// the file's columns to a table's.
package importcsv

import (
	"encoding/csv"
	"io"
	"strings"
	"unicode"
)

// Options says how to read a file; the zero value is comma-separated UTF-8
// without a header.
type Options struct {
	Delimiter rune
	HasHeader bool
	Encoding  string // one of the encoding constants; "" detects a BOM
}

// Reader streams the data records of a file, skipping the header.
type Reader struct {
	cr     *csv.Reader
	header bool
}

func NewReader(r io.Reader, o Options) (*Reader, error) {
	dec, _, err := decode(r, o.Encoding)
	if err != nil {
		return nil, err
	}
	return &Reader{cr: newCSVReader(dec, o.Delimiter), header: o.HasHeader}, nil
}

// Read returns the next record, or io.EOF at the end. The slice is reused
// by the next call.
func (r *Reader) Read() ([]string, error) {
	if r.header {
		r.header = false
		if _, err := r.cr.Read(); err != nil {
			return nil, err
		}
	}
	return r.cr.Read()
}

// Line is the input line of the last record read, for error messages.
func (r *Reader) Line() int {
	line, _ := r.cr.FieldPos(0)
	return line
}

// AutoMap suggests a table column for each file column, "" for none. Names
// match ignoring case, spaces, "_" and "-"; each table column is used once.
// When nothing matches and the counts are equal, columns map by position.
func AutoMap(header, columns []string) []string {
	out := make([]string, len(header))
	byKey := map[string]string{}
	for _, c := range columns {
		if k := mapKey(c); byKey[k] == "" {
			byKey[k] = c
		}
	}
	used := map[string]bool{}
	matched := false
	for i, h := range header {
		if c := byKey[mapKey(h)]; c != "" && !used[c] {
			out[i], used[c], matched = c, true, true
		}
	}
	if !matched && len(header) == len(columns) {
		copy(out, columns)
	}
	return out
}

func mapKey(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}
