package oracle

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// named turns positional arguments into go-ora binds (:1, :2, ...).
func named(args []any) []sqldriver.NamedValue {
	if len(args) == 0 {
		return nil
	}
	out := make([]sqldriver.NamedValue, len(args))
	for i, a := range args {
		out[i] = sqldriver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return out
}

// query runs a statement, calling scan for each row. The row slice is reused
// between calls.
func (l *link) query(ctx context.Context, sql string, args []any, scan func(row []sqldriver.Value) error) error {
	rows, err := l.oc.QueryContext(ctx, sql, named(args))
	if err != nil {
		return err
	}
	defer rows.Close()
	dest := make([]sqldriver.Value, len(rows.Columns()))
	for {
		err := rows.Next(dest)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := scan(dest); err != nil {
			return err
		}
	}
}

var errNoRows = errors.New("no rows")

// queryRow scans the first row into dest pointers (*string, *int64,
// *float64, *bool, **time.Time, *any); NULL becomes the zero value.
func (l *link) queryRow(ctx context.Context, sql string, args []any, dest ...any) error {
	found := false
	err := l.query(ctx, sql, args, func(row []sqldriver.Value) error {
		if found {
			return nil
		}
		found = true
		return scanRow(row, dest...)
	})
	if err == nil && !found {
		return errNoRows
	}
	return err
}

func (l *link) exec(ctx context.Context, sql string, args ...any) (int64, error) {
	res, err := l.oc.ExecContext(ctx, sql, named(args))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func scanRow(row []sqldriver.Value, dest ...any) error {
	if len(dest) > len(row) {
		return fmt.Errorf("scan: %d columns, %d destinations", len(row), len(dest))
	}
	for i, d := range dest {
		v := row[i]
		switch p := d.(type) {
		case *string:
			*p = str(v)
		case *int64:
			*p = integer(v)
		case *float64:
			*p = float(v)
		case *bool:
			*p = str(v) == "Y" || str(v) == "YES" || integer(v) != 0
		case **time.Time:
			*p = nil
			if t, ok := v.(time.Time); ok {
				*p = &t
			}
		case *any:
			*p = v
		default:
			return fmt.Errorf("scan: unsupported destination %T", d)
		}
	}
	return nil
}

// str renders a catalog value as text; NULL is "".
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// integer converts a catalog number; NULL and garbage are 0.
func integer(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

func float(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}
