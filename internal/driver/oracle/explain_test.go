package oracle

import "testing"

func TestBuildPlan(t *testing.T) {
	root, err := buildPlan([]planRow{
		{id: 0, parent: -1, op: "SELECT STATEMENT", cost: 5, hasCost: true, card: 2, hasCard: true},
		{id: 1, parent: 0, op: "HASH JOIN", cost: 5, hasCost: true, access: `"O"."CUSTOMER_ID"="C"."ID"`},
		{id: 2, parent: 1, op: "TABLE ACCESS", options: "FULL", owner: "IT", object: "CUSTOMERS", alias: "C@SEL$1", bytes: 40},
		{id: 3, parent: 1, op: "INDEX", options: "RANGE SCAN", owner: "IT", object: "ORDERS_IDX", alias: "ORDERS_IDX@SEL$1", filter: "x>1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if root.Op != "SELECT STATEMENT" || root.Cost != 5 || root.Rows != 2 || root.Actual != -1 || root.ActRows != -1 || len(root.Children) != 1 {
		t.Fatalf("root %+v", root)
	}
	join := root.Children[0]
	if join.Op != "HASH JOIN" || join.Props["Access"] == "" || len(join.Children) != 2 {
		t.Fatalf("join %+v", join)
	}
	full, idx := join.Children[0], join.Children[1]
	if full.Op != "TABLE ACCESS FULL" || full.Target != "IT.CUSTOMERS C" || full.Props["Bytes"] != "40" {
		t.Errorf("full scan %+v", full)
	}
	if idx.Target != "IT.ORDERS_IDX" || idx.Props["Filter"] != "x>1" {
		t.Errorf("index %+v", idx)
	}

	if _, err := buildPlan(nil); err == nil {
		t.Error("empty plan accepted")
	}
	if _, err := buildPlan([]planRow{{id: 1, parent: 5}}); err == nil {
		t.Error("orphan row accepted")
	}
}
