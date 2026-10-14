package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

// render prints a plan one node per line: op [target] cost rows actual act-rows.
func render(n *driver.PlanNode) string {
	var b strings.Builder
	var walk func(*driver.PlanNode, int)
	walk = func(n *driver.PlanNode, depth int) {
		fmt.Fprintf(&b, "%s%s [%s] %g %g %g %g\n", strings.Repeat("  ", depth), n.Op, n.Target, n.Cost, n.Rows, n.Actual, n.ActRows)
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return b.String()
}

// at follows child indexes from the root.
func at(n *driver.PlanNode, path ...int) *driver.PlanNode {
	for _, i := range path {
		n = n.Children[i]
	}
	return n
}

type propCheck struct {
	path  []int
	props map[string]string // "" means absent
}

var planFixtures = map[string]struct {
	tree   string
	checks []propCheck
}{
	"plan_analyze_join.json": {
		tree: `Limit [] 3920.77 10 15.567 10
  Sort [] 3922 500 15.566 10
    HashAggregate [] 3909.94 500 15.525 100
      Hash Join [] 3867.3 4852 13.797 10000
        Seq Scan [public.orders o] 3774 19407 11.636 20000
        Hash [] 36 500 0.186 500
          Seq Scan [public.customers c] 36 500 0.136 500
`,
		checks: []propCheck{
			{nil, map[string]string{
				"Planning Time": "0.446 ms", "Execution Time": "15.611 ms",
				"Planning Buffers": "shared hit=280", "Buffers": "shared hit=1288",
				"Settings": "", "Output": "", "Loops": "",
			}},
			{[]int{0}, map[string]string{"Sort Key": "(sum(o.total)) DESC", "Sort Method": "top-N heapsort", "Sort Space": "26 kB (Memory)"}},
			{[]int{0, 0}, map[string]string{"Group Key": "c.name", "Batches": "1", "Memory": "105 kB", "Disk": "", "Planned Partitions": ""}},
			{[]int{0, 0, 0}, map[string]string{"Hash Cond": "(o.customer_id = c.id)", "Inner Unique": ""}},
			{[]int{0, 0, 0, 0}, map[string]string{
				"Filter": "(o.status = 'new'::text)", "Rows Removed by Filter": "180000",
				"Buffers": "shared hit=1274", "Shared Hit Blocks": "",
			}},
			{[]int{0, 0, 0, 1}, map[string]string{"Buckets": "1024", "Batches": "1", "Memory": "30 kB"}},
		},
	},
	"plan_index.json": {
		tree: `Sort [] 317.6 100 -1 -1
  Bitmap Heap Scan [public.orders o] 314.02 100 -1 -1
    Bitmap Index Scan [orders_customer_idx] 5.04 100 -1 -1
`,
		checks: []propCheck{
			{nil, map[string]string{"Sort Key": "o.id DESC", "Planning Buffers": "shared hit=103", "Buffers": "", "Planning Time": ""}},
			{[]int{0}, map[string]string{"Recheck Cond": "(o.customer_id = 42)"}},
			{[]int{0, 0}, map[string]string{"Index Cond": "(o.customer_id = 42)", "Index": ""}},
		},
	},
	"plan_never.json": {
		tree: `Nested Loop Semi Join [] 14.36 1 0.007 0
  Index Scan [customers c] 8.29 1 0.006 0
  Index Only Scan [orders o] 6.04 100 0 0
`,
		checks: []propCheck{
			{nil, map[string]string{"Planning Time": "0.283 ms", "Execution Time": "0.033 ms", "Never executed": ""}},
			{[]int{0}, map[string]string{"Index": "customers_pkey", "Index Cond": "(id = '-1'::integer)", "Rows Removed by Index Recheck": ""}},
			{[]int{1}, map[string]string{"Never executed": "yes", "Index": "orders_customer_idx", "Heap Fetches": "0", "Loops": ""}},
		},
	},
	"plan_parallel_cte.json": {
		tree: `Aggregate [] 3710.55 1 20.74 1
  Gather [] 2744.59 39126 9.098 39600
    Parallel Seq Scan [public.orders] 2744.59 23015 19.726 39600
  Hash Left Join [] 941.5 9782 19.249 10000
    CTE Scan [big] 782.52 39126 12.905 39600
    Hash [] 31 2000 0.335 2000
      Seq Scan [public.customers c] 31 2000 0.148 2000
`,
		checks: []propCheck{
			{nil, map[string]string{"Settings": "parallel_setup_cost=0, parallel_tuple_cost=0", "Execution Time": "21.183 ms"}},
			{[]int{0}, map[string]string{"Subplan Name": "CTE big", "Workers": "1 planned, 1 launched", "Single Copy": ""}},
			{[]int{0, 0}, map[string]string{"Loops": "2", "Rows Removed by Filter": "160400", "Filter": "(orders.total > '400'::numeric)"}},
			{[]int{1}, map[string]string{"Filter": "((c.id IS NULL) OR (c.region = 'e'::text))", "Rows Removed by Filter": "29600"}},
		},
	},
	"plan_update.json": {
		tree: `Update [public.orders] 37.49 3 0.112 3
  Nested Loop [] 37.49 3 0.041 3
    HashAggregate [] 12.17 3 0.03 3
      Subquery Scan [ANY_subquery] 12.14 3 0.026 3
        Limit [] 12.11 3 0.013 3
          Index Scan [public.orders orders_1] 394.01 100 0.012 3
    Index Scan [public.orders] 8.44 1 0.009 3
`,
		checks: []propCheck{
			{nil, map[string]string{"Operation": "", "Buffers": "shared hit=57", "Execution Time": "0.172 ms"}},
			{[]int{0, 0}, map[string]string{"Group Key": `"ANY_subquery".id`}},
			{[]int{0, 1}, map[string]string{"Loops": "3", "Index": "orders_pkey", "Index Cond": `(orders.id = "ANY_subquery".id)`}},
		},
	},
}

func TestParsePlanFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "plan_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(planFixtures) {
		t.Errorf("%d fixtures on disk, %d with expectations", len(files), len(planFixtures))
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			want, ok := planFixtures[name]
			if !ok {
				t.Fatal("no expectations for fixture")
			}
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			root, err := parsePlan(data)
			if err != nil {
				t.Fatal(err)
			}
			if got := render(root); got != want.tree {
				t.Errorf("tree:\n%s\nwant:\n%s", got, want.tree)
			}
			for _, c := range want.checks {
				n := at(root, c.path...)
				for k, v := range c.props {
					if got := n.Props[k]; got != v {
						t.Errorf("%v %s %q: got %q, want %q", c.path, n.Op, k, got, v)
					}
				}
			}
		})
	}
}

func TestParsePlanShapes(t *testing.T) {
	data := `[{"Plan": {"Node Type": "Append", "Total Cost": 1, "Plan Rows": 2, "Plans": [
		{"Node Type": "Aggregate", "Strategy": "Sorted", "Partial Mode": "Finalize"},
		{"Node Type": "Aggregate", "Strategy": "Hashed", "Partial Mode": "Partial", "Parallel Aware": false},
		{"Node Type": "Hash Join", "Join Type": "Anti", "Parallel Aware": true},
		{"Node Type": "Merge Join", "Join Type": "Full"},
		{"Node Type": "Nested Loop", "Join Type": "Left"},
		{"Node Type": "SetOp", "Strategy": "Hashed", "Command": "Except"},
		{"Node Type": "ModifyTable", "Operation": "Insert", "Relation Name": "t", "Schema": "s", "Alias": "t",
		 "Conflict Resolution": "NOTHING", "Conflict Arbiter Indexes": ["t_pkey"]},
		{"Node Type": "Index Scan", "Scan Direction": "Backward", "Index Name": "i", "Relation Name": "t", "Alias": "x"},
		{"Node Type": "Function Scan", "Function Name": "generate_series", "Schema": "pg_catalog", "Alias": "g"},
		{"Node Type": "Foreign Scan", "Async Capable": true, "Relation Name": "f"},
		{"Node Type": "Custom Scan", "Custom Plan Provider": "Citus Adaptive"},
		{"Node Type": "Values Scan", "Alias": "*VALUES*"},
		{"Node Type": "Hash", "Hash Buckets": 4096, "Original Hash Buckets": 1024, "Hash Batches": 2, "Original Hash Batches": 1, "Disk Usage": 12},
		{"Node Type": "Seq Scan", "Relation Name": "t", "Actual Loops": 4, "Actual Rows": 2.5, "Actual Total Time": 0.1,
		 "Workers": [{"Worker Number": 0}], "Temp Read Blocks": 3, "Temp Written Blocks": 4, "Local Hit Blocks": 1}
	]}, "Triggers": [{"Trigger Name": "audit", "Relation": "t", "Time": 1.25, "Calls": 3}],
	   "Settings": {"search_path": "\"shop\", public", "work_mem": "64MB"},
	   "JIT": {"Functions": 7, "Timing": {"Total": 12.5}}}]`
	root, err := parsePlan([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := `Append [] 1 2 -1 -1
  Finalize GroupAggregate [] 0 0 -1 -1
  Partial HashAggregate [] 0 0 -1 -1
  Parallel Hash Anti Join [] 0 0 -1 -1
  Merge Full Join [] 0 0 -1 -1
  Nested Loop Left Join [] 0 0 -1 -1
  HashSetOp Except [] 0 0 -1 -1
  Insert [s.t] 0 0 -1 -1
  Index Scan Backward [t x] 0 0 -1 -1
  Function Scan [pg_catalog.generate_series g] 0 0 -1 -1
  Async Foreign Scan [f] 0 0 -1 -1
  Custom Scan (Citus Adaptive) [] 0 0 -1 -1
  Values Scan [*VALUES*] 0 0 -1 -1
  Hash [] 0 0 -1 -1
  Seq Scan [t] 0 0 0.4 10
`
	if got := render(root); got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	checks := map[*driver.PlanNode]map[string]string{
		root:         {"Trigger audit": "3 calls, 1.25 ms", "JIT": "7 functions, 12.5 ms", "Settings": "work_mem=64MB"},
		at(root, 6):  {"Conflict Resolution": "NOTHING", "Conflict Arbiter Indexes": "t_pkey"},
		at(root, 7):  {"Index": "i"},
		at(root, 12): {"Buckets": "4096 (originally 1024)", "Batches": "2 (originally 1)", "Disk": "12 kB"},
		at(root, 13): {"Loops": "4", "Buffers": "local hit=1, temp read=3 written=4", "Workers": ""},
	}
	for n, props := range checks {
		for k, v := range props {
			if got := n.Props[k]; got != v {
				t.Errorf("%s %q: got %q, want %q (all: %v)", n.Op, k, got, v, n.Props)
			}
		}
	}
}

func TestParsePlanMultipleAndBad(t *testing.T) {
	root, err := parsePlan([]byte(`[{"Plan": {"Node Type": "Result"}}, {"Plan": {"Node Type": "Seq Scan", "Relation Name": "t"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if root.Op != "Queries" || len(root.Children) != 2 || root.Children[1].Target != "t" {
		t.Errorf("multiple queries: %s", render(root))
	}
	for _, bad := range []string{``, `{}`, `[]`, `[{"Settings": {}}]`} {
		if _, err := parsePlan([]byte(bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestShiftError(t *testing.T) {
	for _, c := range []struct{ pos, n, want int }{{30, 10, 20}, {5, 10, -1}, {-1, 10, -1}} {
		err := shiftError(&driver.QueryError{Position: c.pos}, c.n).(*driver.QueryError)
		if err.Position != c.want {
			t.Errorf("%d-%d: %d, want %d", c.pos, c.n, err.Position, c.want)
		}
	}
}
