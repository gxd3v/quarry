package postgres

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

// rowsOf returns a next function over rows.
func rowsOf(rows [][]any) func() ([]any, error) {
	i := 0
	return func() ([]any, error) {
		if i == len(rows) {
			return nil, io.EOF
		}
		i++
		return rows[i-1], nil
	}
}

func TestITImport(t *testing.T) {
	c := openEd(t, false)
	pc := itConn(t, edDB)
	ctx := context.Background()
	if _, err := pc.Exec(ctx, `DROP TABLE IF EXISTS ed.imp; CREATE TABLE ed.imp (id int PRIMARY KEY, name text, price numeric, at date, tags text[])`); err != nil {
		t.Fatal(err)
	}
	node := edNode(folderTables, "imp")
	im := c.(driver.Importer)

	const n = 100_000
	i := 0
	var last atomic.Int64
	got, err := im.Import(ctx, node, []string{"id", "name", "price", "at", "tags"}, func() ([]any, error) {
		if i == n {
			return nil, io.EOF
		}
		i++
		name := any(`say "hi", ` + strconv.Itoa(i))
		if i == 2 {
			name = ""
		} else if i == 3 {
			name = nil
		}
		return []any{strconv.Itoa(i), name, "1.50", "2024-02-29", "{a,b}"}, nil
	}, func(r int64) { last.Store(r) })
	if err != nil {
		t.Fatal(err)
	}
	if got != n || last.Load() != n {
		t.Fatalf("imported %d, progress %d", got, last.Load())
	}
	var count int
	var empty, null bool
	var name string
	err = pc.QueryRow(ctx, `SELECT count(*), (SELECT name = '' FROM ed.imp WHERE id = 2), (SELECT name IS NULL FROM ed.imp WHERE id = 3),
		(SELECT name FROM ed.imp WHERE id = 1) FROM ed.imp WHERE price = 1.5 AND at = '2024-02-29' AND tags = '{a,b}'`).Scan(&count, &empty, &null, &name)
	if err != nil || count != n || !empty || !null || name != `say "hi", 1` {
		t.Fatalf("count %d empty %v null %v name %q err %v", count, empty, null, name, err)
	}

	// A bad value aborts the whole import.
	_, err = im.Import(ctx, node, []string{"id", "price"}, rowsOf([][]any{{"200001", "1"}, {"200002", "abc"}}), nil)
	if err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("bad value: %v", err)
	}
	_, err = im.Import(ctx, node, []string{"id", "price"}, rowsOf([][]any{{"200001", "1"}, {"200002"}}), nil)
	if err == nil || !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("short row: %v", err)
	}
	if err := pc.QueryRow(ctx, "SELECT count(*) FROM ed.imp").Scan(&count); err != nil || count != n {
		t.Fatalf("rows after failed imports: %d %v", count, err)
	}

	// Cancelling rolls back.
	cctx, cancel := context.WithCancel(ctx)
	j := 0
	_, err = im.Import(cctx, node, []string{"id"}, func() ([]any, error) {
		j++
		if j == 50_000 {
			cancel()
		}
		return []any{strconv.Itoa(300_000 + j)}, nil
	}, nil)
	if err == nil {
		t.Fatal("cancelled import succeeded")
	}
	if err := pc.QueryRow(ctx, "SELECT count(*) FROM ed.imp").Scan(&count); err != nil || count != n {
		t.Fatalf("rows after cancel: %d %v", count, err)
	}

	if _, err := openEd(t, true).(driver.Importer).Import(ctx, node, []string{"id"}, rowsOf(nil), nil); err == nil {
		t.Error("read-only import succeeded")
	}
	if _, err := im.Import(ctx, edNode(folderViews, "cheap"), []string{"id"}, rowsOf(nil), nil); err == nil {
		t.Error("import into a view succeeded")
	}
}
