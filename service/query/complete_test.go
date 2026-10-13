package query

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/complete"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
	"github.com/gxd3v/quarry/service/catalog"
)

var shopObjects = []driver.Object{
	{Schema: "public", Name: "users", Kind: driver.NodeTable, Columns: []driver.Column{{Name: "id"}, {Name: "name"}, {Name: "email"}}},
	{Schema: "shop", Name: "orders", Kind: driver.NodeTable, Columns: []driver.Column{{Name: "id"}, {Name: "total"}}},
}

// newCompleteEnv is a SQL env whose fake connection serves shopObjects.
func newCompleteEnv(t *testing.T, stmts map[string]fakeStmt) *env {
	t.Helper()
	e := newEnv(t, stmts)
	e.conn.objs = shopObjects
	e.s.catalog = catalog.New(e.live)
	return e
}

// completeAt completes text at the '|' marker, given in UTF-16 units.
func (e *env) completeAt(t *testing.T, tab, text string) (*quarryv1.CompleteResponse, string) {
	t.Helper()
	i := strings.Index(text, "|")
	script := text[:i] + text[i+1:]
	res, err := e.s.Complete(context.Background(), connect.NewRequest(&quarryv1.CompleteRequest{
		TabId: tab, Script: script, Cursor: int32(service.UTF16Offset(script, i)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg, script
}

func itemLabels(r *quarryv1.CompleteResponse) []string {
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Label)
	}
	return out
}

func item(r *quarryv1.CompleteResponse, label string) *quarryv1.CompletionItem {
	for _, it := range r.Items {
		if it.Label == label {
			return it
		}
	}
	return nil
}

func TestCompleteTablesAndColumns(t *testing.T) {
	e := newCompleteEnv(t, nil)

	r, _ := e.completeAt(t, e.tab, "select * from us|")
	if it := item(r, "users"); it == nil || it.Kind != "table" || it.Insert != "users" {
		t.Fatalf("tables: %v", r.Items)
	}
	if r.Start != 14 || r.End != 16 {
		t.Errorf("range %d..%d, want 14..16", r.Start, r.End)
	}

	r, _ = e.completeAt(t, e.tab, "select u.| from users u")
	if got := itemLabels(r); !slices.Equal(slices.Sorted(slices.Values(got)), []string{"email", "id", "name"}) {
		t.Fatalf("columns: %v", got)
	}
	if r.Items[0].Kind != "column" || r.Start != 9 || r.End != 9 {
		t.Errorf("columns: %v at %d..%d", r.Items[0], r.Start, r.End)
	}

	// A tab scoped to a schema resolves its objects unqualified.
	res, err := e.s.OpenTab(context.Background(), connect.NewRequest(&quarryv1.OpenTabRequest{ConnId: "pg", Scope: &quarryv1.Scope{Schema: "shop"}}))
	if err != nil {
		t.Fatal(err)
	}
	r, _ = e.completeAt(t, res.Msg.TabId, "select * from ord|")
	if item(r, "orders") == nil {
		t.Errorf("scoped tab: %v", itemLabels(r))
	}
	r, _ = e.completeAt(t, e.tab, "select * from ord|")
	if item(r, "orders") != nil || item(r, "shop.orders") == nil {
		t.Errorf("unscoped tab: %v", itemLabels(r))
	}
}

func TestCompleteUTF16Offsets(t *testing.T) {
	e := newCompleteEnv(t, nil)
	r, script := e.completeAt(t, e.tab, "select '\U0001F600é', u.na| from users u")
	start := service.UTF16Offset(script, strings.Index(script, "na "))
	if int(r.Start) != start || int(r.End) != start+2 {
		t.Fatalf("range %d..%d, want %d..%d", r.Start, r.End, start, start+2)
	}
	if start != 16 { // the emoji counts as two units, é as one
		t.Fatalf("start = %d", start)
	}
	if it := item(r, "name"); it == nil || it.Kind != "column" {
		t.Fatalf("items %v", r.Items)
	}
}

func TestCompleteWhileRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newCompleteEnv(t, map[string]fakeStmt{"select pg_sleep(30)": {blockRun: true}})
		run := e.runBG(context.Background(), "select pg_sleep(30)")
		synctest.Wait()

		r, _ := e.completeAt(t, e.tab, "select * from us|")
		if item(r, "users") == nil {
			t.Errorf("items %v", itemLabels(r))
		}
		select {
		case <-run.done:
			t.Fatal("run ended before cancel")
		default:
		}
		e.cancel(t)
		<-run.done
	})
}

func TestCompleteKeywordsOnly(t *testing.T) {
	e := newCompleteEnv(t, nil)
	e.conn.mu.Lock()
	e.conn.objErr = errors.New("permission denied")
	e.conn.mu.Unlock()
	e.live.add("down", driver.Postgres, nil)
	down := e.open(t, "down")
	bare := newEnv(t, nil) // no catalog

	for name, c := range map[string]struct {
		e   *env
		tab string
	}{"catalog fails": {e, e.tab}, "not connected": {e, down}, "no catalog": {bare, bare.tab}} {
		r, _ := c.e.completeAt(t, c.tab, "sel|")
		if it := item(r, "select"); it == nil || it.Kind != "keyword" {
			t.Errorf("%s: keywords %v", name, itemLabels(r))
		}
		r, _ = c.e.completeAt(t, c.tab, "select * from us|")
		if item(r, "users") != nil {
			t.Errorf("%s: objects %v", name, itemLabels(r))
		}
	}

	if _, err := e.s.Complete(context.Background(), connect.NewRequest(&quarryv1.CompleteRequest{TabId: "nope"})); code(err) != connect.CodeNotFound {
		t.Errorf("unknown tab: %v", err)
	}
}

func TestCompleteKindNames(t *testing.T) {
	// The kinds listed in quarry.v1.CompletionItem.
	want := []string{"keyword", "schema", "table", "view", "column", "function", "alias", "snippet", "collection", "command", "key"}
	for i, w := range want {
		if got := complete.Kind(i).String(); got != w {
			t.Errorf("kind %d = %q, want %q", i, got, w)
		}
	}
	if complete.Kind(len(want)).String() != "unknown" {
		t.Error("complete has kinds the protocol does not list")
	}
}

func TestSearchPath(t *testing.T) {
	for _, tc := range []struct {
		kind  driver.Kind
		scope driver.Scope
		want  []string
	}{
		{driver.Postgres, driver.Scope{}, []string{"public"}},
		{driver.Postgres, driver.Scope{Schema: "shop"}, []string{"shop", "public"}},
		{driver.Postgres, driver.Scope{Schema: "public"}, []string{"public"}},
		{driver.Redis, driver.Scope{Database: "0"}, nil},
	} {
		if got := searchPath(tc.kind, tc.scope); !slices.Equal(got, tc.want) {
			t.Errorf("%s %+v: %v, want %v", tc.kind, tc.scope, got, tc.want)
		}
	}
}
