// Package export streams query results to files and writers as CSV, TSV,
// JSON, NDJSON, SQL INSERT statements or Markdown tables. Its value
// formatting helpers are shared with clipboard copies so both agree.
package export

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

type Format int

const (
	CSV Format = iota
	TSV
	JSON // array of objects, one per line
	NDJSON
	SQLInsert
	Markdown
)

var formatNames = [...]string{"CSV", "TSV", "JSON", "NDJSON", "SQL INSERT", "Markdown"}

func (f Format) String() string {
	if f >= 0 && int(f) < len(formatNames) {
		return formatNames[f]
	}
	return "Format(?)"
}

// Ext is the conventional file extension for f, with the dot.
func (f Format) Ext() string {
	return [...]string{".csv", ".tsv", ".json", ".ndjson", ".sql", ".md"}[f]
}

type Options struct {
	Format    Format
	Delimiter rune   // CSV only; default ','
	Header    bool   // CSV/TSV/Markdown
	NullText  string // CSV/TSV representation of NULL
	QuoteAll  bool   // CSV: quote every non-NULL field
	CRLF      bool   // line endings
	BOM       bool   // UTF-8 byte order mark, for Excel
	Table     string // SQLInsert target, verbatim (may be schema-qualified); default "t"
	Dialect   string // SQLInsert literal syntax: "postgres", "oracle" or ""
	BatchRows int    // SQLInsert rows per statement; default 1
}

// DefaultOptions returns the defaults for f: a header row, ',' delimiter,
// CRLF line ends for CSV only (RFC 4180), one INSERT per row into "t".
func DefaultOptions(f Format) Options {
	return Options{Format: f, Delimiter: ',', Header: true, CRLF: f == CSV, Table: "t", BatchRows: 1}
}

// FormatFromExt guesses the format from a file name's extension.
func FormatFromExt(path string) (Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return CSV, true
	case ".tsv", ".tab":
		return TSV, true
	case ".json":
		return JSON, true
	case ".ndjson", ".jsonl":
		return NDJSON, true
	case ".sql":
		return SQLInsert, true
	case ".md", ".markdown":
		return Markdown, true
	}
	return 0, false
}

// Source yields rows in pages of up to n; an empty page means done.
// driver.Result.Next fits it.
type Source func(ctx context.Context, n int) ([][]any, error)

// Progress is told the number of rows written so far.
type Progress func(rows int64)

const (
	pageSize = 1000
	bom      = "\xef\xbb\xbf"
)

// Write streams the rows of src to w in opts.Format, holding at most one page
// in memory. It stops with ctx's error between pages. progress, if not nil,
// is called at most every pageSize rows and once at the end.
func Write(ctx context.Context, w io.Writer, cols []driver.Column, src Source, opts Options, progress Progress) (rows int64, err error) {
	opts, err = normalize(opts)
	if err != nil {
		return 0, err
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	if opts.BOM {
		bw.WriteString(bom)
	}
	fw := newFormatter(bw, cols, opts)
	var reported int64
	for {
		if err := ctx.Err(); err != nil {
			return rows, err
		}
		page, err := src(ctx, pageSize)
		if err != nil {
			return rows, err
		}
		if len(page) == 0 {
			break
		}
		if err := fw.page(page); err != nil {
			return rows, err
		}
		if err := bw.Flush(); err != nil {
			return rows, err
		}
		rows += int64(len(page))
		if progress != nil && rows-reported >= pageSize {
			progress(rows)
			reported = rows
		}
	}
	if err := fw.finish(); err != nil {
		return rows, err
	}
	if err := bw.Flush(); err != nil {
		return rows, err
	}
	if progress != nil && rows != reported {
		progress(rows)
	}
	return rows, nil
}

func normalize(o Options) (Options, error) {
	if o.Format < CSV || o.Format > Markdown {
		return o, errors.New("export: unknown format")
	}
	switch {
	case o.Format == TSV:
		o.Delimiter = '\t'
	case o.Delimiter == 0:
		o.Delimiter = ','
	case o.Delimiter == '"' || o.Delimiter == '\r' || o.Delimiter == '\n' || !utf8.ValidRune(o.Delimiter):
		return o, errors.New("export: invalid delimiter")
	}
	if o.Table == "" {
		o.Table = "t"
	}
	o.BatchRows = max(o.BatchRows, 1)
	return o, nil
}

// WriteFile writes the export to path atomically: rows go to a temporary
// file in the same directory that replaces path only on success, and is
// removed on error or cancellation.
func WriteFile(ctx context.Context, path string, cols []driver.Column, src Source, opts Options, progress Progress) (int64, error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return 0, err
	}
	tmp := f.Name()
	rows, err := Write(ctx, f, cols, src, opts, progress)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return rows, err
	}
	return rows, nil
}
