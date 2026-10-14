package postgres

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Importer = (*conn)(nil)

// progressEvery is how many rows pass between progress calls.
const progressEvery = 1000

// Import loads rows with COPY ... FROM STDIN (FORMAT csv) in one
// transaction, so the server converts the text to the column types. Every
// value is quoted, which keeps empty strings apart from NULLs (unquoted
// empty fields).
func (c *conn) Import(ctx context.Context, target *driver.Node, columns []string, next func() ([]any, error), progress func(int64)) (int64, error) {
	if c.readOnly {
		return 0, errReadOnly
	}
	if c.closed.Load() {
		return 0, errClosed
	}
	folder, schema, name, err := relation(target)
	if err != nil {
		return 0, err
	}
	if folder != folderTables {
		return 0, fmt.Errorf("%s.%s is not a table: rows cannot be imported into it", schema, name)
	}
	if len(columns) == 0 {
		return 0, errors.New("no columns to import")
	}
	pc, err := c.connect(ctx, target.Path[0], nil, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		pc.Close(cctx)
	}()

	tx, err := pc.Begin(ctx)
	if err != nil {
		return 0, queryError(err, "")
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		tx.Rollback(rctx)
	}()

	cols := make([]string, len(columns))
	for i, col := range columns {
		cols[i] = pgx.Identifier{col}.Sanitize()
	}
	sql := fmt.Sprintf("COPY %s (%s) FROM STDIN (FORMAT csv)", pgx.Identifier{schema, name}.Sanitize(), strings.Join(cols, ", "))

	pr, pw := io.Pipe()
	var rows int64
	written := make(chan error, 1)
	go func() {
		err := writeCSV(pw, len(columns), next, func(n int64) {
			rows = n
			if progress != nil && n%progressEvery == 0 {
				progress(n)
			}
		})
		pw.CloseWithError(err)
		written <- err
	}()
	_, err = pc.PgConn().CopyFrom(ctx, pr, sql)
	pr.Close() // stops the writer when COPY ended first
	if werr := <-written; errors.Is(werr, errRow) {
		return 0, werr
	}
	if err != nil {
		return 0, queryError(err, "")
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, queryError(err, "")
	}
	if progress != nil {
		progress(rows)
	}
	return rows, nil
}

// errRow marks errors in the rows given to Import.
var errRow = errors.New("row")

// writeCSV writes the rows from next as CSV with every value quoted.
func writeCSV(w io.Writer, width int, next func() ([]any, error), count func(int64)) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	var n int64
	for {
		row, err := next()
		if err == io.EOF {
			return bw.Flush()
		}
		if err != nil {
			return fmt.Errorf("%w %d: %w", errRow, n+1, err)
		}
		if len(row) != width {
			return fmt.Errorf("%w %d: %d values for %d columns", errRow, n+1, len(row), width)
		}
		for i, v := range row {
			if i > 0 {
				bw.WriteByte(',')
			}
			switch v := v.(type) {
			case nil:
			case string:
				bw.WriteByte('"')
				bw.WriteString(strings.ReplaceAll(v, `"`, `""`))
				bw.WriteByte('"')
			default:
				return fmt.Errorf("%w %d: unsupported value %T", errRow, n+1, v)
			}
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
		n++
		count(n)
	}
}
