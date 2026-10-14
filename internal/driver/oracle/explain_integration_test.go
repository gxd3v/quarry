package oracle

import (
	"context"
	"errors"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestITExplain(t *testing.T) {
	s := newSession(t, openUser(t), driver.Scope{})
	ex := s.(driver.Explainer)
	ctx := context.Background()

	plan, err := ex.Explain(ctx, driver.Statement{Text: "SELECT * FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.total > 10;"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Op != "SELECT STATEMENT" || len(plan.Children) == 0 || plan.Actual != -1 {
		t.Fatalf("plan %+v", plan)
	}
	if s.(driver.Transactional).InTx() {
		t.Error("explain left a transaction open")
	}

	// Inside the user's transaction, explain must not end it.
	tx := s.(driver.Transactional)
	if err := tx.SetAutoCommit(ctx, false); err != nil {
		t.Fatal(err)
	}
	all(t, s, "UPDATE customers SET name = 'explained' WHERE id = 1")
	if _, err := ex.Explain(ctx, driver.Statement{Text: "DELETE FROM customers"}, false); err != nil {
		t.Fatal(err)
	}
	if _, rows := all(t, s, "SELECT name FROM customers WHERE id = 1"); !tx.InTx() || rows[0][0] != "explained" {
		t.Fatalf("user transaction lost: in tx %v, rows %v", tx.InTx(), rows)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	_, err = ex.Explain(ctx, driver.Statement{Text: "SELECT nope FROM customers"}, false)
	qe, ok := errors.AsType[*driver.QueryError](err)
	if !ok || qe.Position != len("SELECT ") {
		t.Fatalf("error %v, position %+v", err, qe)
	}
	if _, err := ex.Explain(ctx, driver.Statement{Text: "SELECT 1 FROM dual"}, true); err == nil {
		t.Error("analyze accepted")
	}
	if _, err := ex.Explain(ctx, driver.Statement{Text: "BEGIN NULL; END;"}, false); err == nil {
		t.Error("PL/SQL block accepted")
	}
}
