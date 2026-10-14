package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

func find(n *driver.PlanNode, f func(*driver.PlanNode) bool) *driver.PlanNode {
	if f(n) {
		return n
	}
	for _, c := range n.Children {
		if m := find(c, f); m != nil {
			return m
		}
	}
	return nil
}

func TestITExplain(t *testing.T) {
	c := open(t)
	s := newSession(t, c, driver.Scope{Database: itDB, Schema: "shop"})
	ex := s.(driver.Explainer)
	tx := s.(driver.Transactional)
	ctx := context.Background()
	all(t, s, "create temp table explain_t (id int primary key, v text)")
	all(t, s, "insert into explain_t select g, 'v' || g from generate_series(1, 100) g")
	value := func() string {
		_, rows := all(t, s, "select v from explain_t where id = 1")
		return rows[0][0].(string)
	}

	plan, err := ex.Explain(ctx, driver.Statement{Text: "select * from explain_t t where id = 1;"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actual != -1 || plan.ActRows != -1 || plan.Cost <= 0 || plan.Props["Execution Time"] != "" {
		t.Errorf("plain explain: %+v", plan)
	}
	if find(plan, func(n *driver.PlanNode) bool {
		return strings.HasPrefix(n.Target, "pg_temp") && strings.HasSuffix(n.Target, ".explain_t t")
	}) == nil {
		t.Errorf("no scan of explain_t t:\n%s", render(plan))
	}

	plan, err = ex.Explain(ctx, driver.Statement{Text: "select count(*) from explain_t"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actual < 0 || plan.ActRows != 1 || plan.Props["Execution Time"] == "" || plan.Props["Planning Time"] == "" {
		t.Errorf("analyze: %+v", plan)
	}
	if n := find(plan, func(n *driver.PlanNode) bool { return strings.Contains(n.Op, "Scan") }); n == nil || n.ActRows != 100 {
		t.Errorf("analyze scan:\n%s", render(plan))
	}
	if tx.InTx() {
		t.Error("analyze left a transaction open")
	}

	plan, err = ex.Explain(ctx, driver.Statement{Text: "update explain_t set v = 'changed' where id = 1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Op != "Update" || !strings.HasSuffix(plan.Target, ".explain_t") || plan.Actual < 0 {
		t.Errorf("analyze update:\n%s", render(plan))
	}
	if tx.InTx() {
		t.Error("analyze update left a transaction open")
	}
	if v := value(); v != "v1" {
		t.Errorf("analyze update was not rolled back: %q", v)
	}

	_, err = ex.Explain(ctx, driver.Statement{Text: "select nope from explain_t"}, false)
	if qe, ok := errors.AsType[*driver.QueryError](err); !ok || qe.Position != 7 {
		t.Errorf("error position: %#v", err)
	}

	// Inside the user's transaction: the analyzed write and a failing
	// EXPLAIN are undone without touching the transaction's own work.
	if err := tx.SetAutoCommit(ctx, false); err != nil {
		t.Fatal(err)
	}
	all(t, s, "update explain_t set v = 'mine' where id = 1")
	if _, err := ex.Explain(ctx, driver.Statement{Text: "delete from explain_t"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Explain(ctx, driver.Statement{Text: "select 1/0"}, true); err == nil {
		t.Error("expected division by zero")
	}
	if !tx.InTx() {
		t.Fatal("user transaction lost")
	}
	if _, rows := all(t, s, "select count(*) from explain_t"); rows[0][0] != int64(100) {
		t.Errorf("rows after analyze delete in tx: %v", rows)
	}
	if v := value(); v != "mine" {
		t.Errorf("user's update lost: %q", v)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.SetAutoCommit(ctx, true); err != nil {
		t.Fatal(err)
	}

	// Cancelling an analyze rolls back and leaves the session usable.
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := ex.Explain(cctx, driver.Statement{Text: "update explain_t set v = 'late' where id = 1 and pg_sleep(30) is not null"}, true); err == nil {
		t.Fatal("expected cancellation")
	}
	if tx.InTx() {
		t.Error("canceled analyze left a transaction open")
	}
	if v := value(); v != "v1" {
		t.Errorf("after cancel: %q", v)
	}
}
