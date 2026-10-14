package oracle

import (
	"context"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestITCountRows(t *testing.T) {
	c := openUser(t).(driver.Counter)
	node := &driver.Node{Path: []string{itUser, folderTables, "CUSTOMERS"}, Kind: driver.NodeTable, Name: "CUSTOMERS"}
	ctx := context.Background()
	if n, err := c.CountRows(ctx, node, ""); err != nil || n != 2 {
		t.Fatalf("all: %d %v", n, err)
	}
	if n, err := c.CountRows(ctx, node, "name = 'ann'"); err != nil || n != 1 {
		t.Fatalf("filtered: %d %v", n, err)
	}
	if _, err := c.CountRows(ctx, node, "nope = 1"); err == nil {
		t.Fatal("bad filter counted")
	}
}
