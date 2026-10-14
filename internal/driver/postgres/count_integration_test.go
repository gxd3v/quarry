package postgres

import (
	"context"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestITCountRows(t *testing.T) {
	c := openEd(t, false).(driver.Counter)
	ctx := context.Background()
	if _, err := itConn(t, edDB).Exec(ctx, `DROP TABLE IF EXISTS ed.cnt; CREATE TABLE ed.cnt (id int, qty int, sku text); INSERT INTO ed.cnt VALUES (1, 0, 'a'), (2, 5, 'b')`); err != nil {
		t.Fatal(err)
	}
	node := edNode(folderTables, "cnt")
	for _, tc := range []struct {
		filter string
		want   int64
	}{
		{"", 2},
		{"qty > 0", 1},
		{"WHERE sku = 'none'", 0},
	} {
		n, err := c.CountRows(ctx, node, tc.filter)
		if err != nil || n != tc.want {
			t.Errorf("filter %q: %d, %v; want %d", tc.filter, n, err, tc.want)
		}
	}
	if _, err := c.CountRows(ctx, node, "nope = 1"); err == nil {
		t.Error("bad filter counted")
	}
	// The count runs read-only, so a filter cannot write.
	if _, err := c.CountRows(ctx, node, "(SELECT count(*) FROM (DELETE FROM ed.cnt RETURNING 1) d) >= 0"); err == nil {
		t.Error("writing filter counted")
	}
}
