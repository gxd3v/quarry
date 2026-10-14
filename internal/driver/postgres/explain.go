package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Explainer = (*session)(nil)

const explainSavepoint = "quarry_explain"

// Explain runs EXPLAIN (FORMAT JSON) on stmt. Inside an open transaction it
// runs under a savepoint, so a failure does not abort the user's work. With
// analyze the statement is executed and always rolled back: in its own
// transaction, or back to the savepoint.
func (s *session) Explain(ctx context.Context, stmt driver.Statement, analyze bool) (*driver.PlanNode, error) {
	s.closeCur()
	defer s.syncTx()
	if len(keywords(stmt.Text)) == 0 {
		return nil, errors.New("nothing to explain")
	}
	prefix := "EXPLAIN (FORMAT JSON, VERBOSE, SETTINGS) "
	if analyze {
		prefix = "EXPLAIN (FORMAT JSON, VERBOSE, SETTINGS, ANALYZE, BUFFERS) "
	}

	var undo []string
	switch {
	case s.pc.PgConn().TxStatus() == 'T':
		if _, err := s.pc.Exec(ctx, "SAVEPOINT "+explainSavepoint); err != nil {
			return nil, queryError(err, "")
		}
		undo = []string{"ROLLBACK TO SAVEPOINT " + explainSavepoint, "RELEASE SAVEPOINT " + explainSavepoint}
	case analyze && s.pc.PgConn().TxStatus() == 'I':
		if _, err := s.pc.Exec(ctx, "BEGIN"); err != nil {
			return nil, queryError(err, "")
		}
		undo = []string{"ROLLBACK"}
	}

	data, err := s.explainJSON(ctx, prefix+stmt.Text)
	if uerr := s.undo(ctx, undo); uerr != nil {
		return nil, uerr
	}
	if err != nil {
		return nil, shiftError(queryError(err, prefix+stmt.Text), len(prefix))
	}
	return parsePlan(data)
}

func (s *session) explainJSON(ctx context.Context, text string) ([]byte, error) {
	rows, err := s.pc.Query(ctx, text, pgx.QueryExecModeExec)
	if err != nil {
		if rows != nil {
			rows.Close()
		}
		return nil, err
	}
	var data []byte
	if rows.Next() {
		data = slices.Clone(rows.RawValues()[0])
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if data == nil {
		return nil, errors.New("postgres: EXPLAIN returned no plan")
	}
	return data, nil
}

// undo rolls back what Explain opened, even when ctx was canceled. If that
// fails the connection is closed, so that the server discards the work
// rather than leaving it in a transaction the user might commit.
func (s *session) undo(ctx context.Context, stmts []string) error {
	if len(stmts) == 0 {
		return nil
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	for _, q := range stmts {
		if _, err := s.pc.Exec(uctx, q); err != nil {
			s.pc.Close(uctx)
			return fmt.Errorf("postgres: rolling back EXPLAIN: %w", err)
		}
	}
	return nil
}

// shiftError makes a server error position relative to the explained
// statement instead of the EXPLAIN text.
func shiftError(err error, n int) error {
	if qe, ok := errors.AsType[*driver.QueryError](err); ok && qe.Position >= 0 {
		qe.Position -= n
		if qe.Position < 0 {
			qe.Position = -1
		}
	}
	return err
}

// parsePlan converts the output of EXPLAIN (FORMAT JSON) into a tree. Times
// and row counts of analyzed nodes are totals over all loops.
func parsePlan(data []byte) (*driver.PlanNode, error) {
	var queries []map[string]any
	if err := json.Unmarshal(data, &queries); err != nil {
		return nil, fmt.Errorf("postgres: parse plan: %w", err)
	}
	var roots []*driver.PlanNode
	for _, q := range queries {
		p, ok := q["Plan"].(map[string]any)
		if !ok {
			continue
		}
		n := planNode(p)
		queryProps(n.Props, q)
		roots = append(roots, n)
	}
	switch len(roots) {
	case 0:
		return nil, errors.New("postgres: EXPLAIN returned no plan")
	case 1:
		return roots[0], nil
	}
	return &driver.PlanNode{Op: "Queries", Actual: -1, ActRows: -1, Props: map[string]string{}, Children: roots}, nil
}

// consumed lists keys folded into Op, Target and the numbers, combined into
// other props, or not worth showing.
var consumed = map[string]bool{
	"Node Type": true, "Plans": true, "Parent Relationship": true, "Parallel Aware": true,
	"Async Capable": true, "Startup Cost": true, "Total Cost": true, "Plan Rows": true,
	"Plan Width": true, "Actual Startup Time": true, "Actual Total Time": true,
	"Actual Rows": true, "Actual Loops": true, "Output": true, "Relation Name": true,
	"Schema": true, "Alias": true, "Index Name": true, "CTE Name": true, "Function Name": true,
	"Join Type": true, "Strategy": true, "Partial Mode": true, "Operation": true,
	"Command": true, "Custom Plan Provider": true, "Scan Direction": true,
	"Inner Unique": true, "Single Copy": true, "Workers": true, "Workers Planned": true,
	"Workers Launched": true, "Sort Space Used": true, "Sort Space Type": true,
	"Peak Memory Usage": true, "Disk Usage": true, "Hash Buckets": true,
	"Original Hash Buckets": true, "Hash Batches": true, "Original Hash Batches": true,
	"HashAgg Batches": true, "Planned Partitions": true,
}

func planNode(p map[string]any) *driver.PlanNode {
	n := &driver.PlanNode{
		Op:      opName(p),
		Target:  target(p),
		Cost:    num(p, "Total Cost"),
		Rows:    num(p, "Plan Rows"),
		Actual:  -1,
		ActRows: -1,
		Props:   map[string]string{},
	}
	loops, analyzed := p["Actual Loops"].(float64)
	switch {
	case analyzed && loops == 0:
		n.Actual, n.ActRows = 0, 0
		n.Props["Never executed"] = "yes"
	case analyzed:
		if t, ok := p["Actual Total Time"].(float64); ok {
			n.Actual = math.Round(t*loops*1000) / 1000
		}
		if r, ok := p["Actual Rows"].(float64); ok {
			n.ActRows = math.Round(r * loops)
		}
		if loops != 1 {
			n.Props["Loops"] = fmtNum(loops)
		}
	}

	for k, v := range p {
		if consumed[k] || bufferKey(k) {
			continue
		}
		s, ok := propValue(v)
		if !ok {
			continue
		}
		if f, isNum := v.(float64); isNum {
			if f == 0 && (strings.HasPrefix(k, "Rows Removed by ") || strings.Contains(k, "I/O") || k == "Lossy Heap Blocks") {
				continue
			}
			if strings.HasPrefix(k, "Rows Removed by ") && loops > 1 {
				s = fmtNum(math.Round(f * loops))
			}
		}
		n.Props[k] = s
	}
	if _, ok := p["Relation Name"]; ok {
		if idx := str(p, "Index Name"); idx != "" {
			n.Props["Index"] = idx
		}
	}
	if w, ok := p["Workers Planned"].(float64); ok {
		s := fmtNum(w) + " planned"
		if l, ok := p["Workers Launched"].(float64); ok {
			s += ", " + fmtNum(l) + " launched"
		}
		n.Props["Workers"] = s
	}
	if u, ok := p["Sort Space Used"].(float64); ok {
		n.Props["Sort Space"] = fmtNum(u) + " kB"
		if t := str(p, "Sort Space Type"); t != "" {
			n.Props["Sort Space"] += " (" + t + ")"
		}
	}
	if b := changed(p, "Hash Buckets", "Original Hash Buckets"); b != "" {
		n.Props["Buckets"] = b
	}
	if b := changed(p, "Hash Batches", "Original Hash Batches"); b != "" {
		n.Props["Batches"] = b
	} else if b, ok := p["HashAgg Batches"].(float64); ok {
		n.Props["Batches"] = fmtNum(b)
	}
	if m, ok := p["Peak Memory Usage"].(float64); ok {
		n.Props["Memory"] = fmtNum(m) + " kB"
	}
	if d, ok := p["Disk Usage"].(float64); ok && d > 0 {
		n.Props["Disk"] = fmtNum(d) + " kB"
	}
	if b := buffers(p); b != "" {
		n.Props["Buffers"] = b
	}

	if kids, ok := p["Plans"].([]any); ok {
		for _, k := range kids {
			if c, ok := k.(map[string]any); ok {
				n.Children = append(n.Children, planNode(c))
			}
		}
	}
	return n
}

// queryProps adds the statement-level properties to the root's.
func queryProps(props map[string]string, q map[string]any) {
	for _, k := range []string{"Planning Time", "Execution Time"} {
		if t, ok := q[k].(float64); ok {
			props[k] = fmtMs(t)
		}
	}
	if pl, ok := q["Planning"].(map[string]any); ok {
		if b := buffers(pl); b != "" {
			props["Planning Buffers"] = b
		}
	}
	// search_path is left out: sessions set it to their scope.
	if set, ok := q["Settings"].(map[string]any); ok {
		var kv []string
		for _, k := range slices.Sorted(maps.Keys(set)) {
			if k != "search_path" {
				v, _ := propValue(set[k])
				kv = append(kv, k+"="+v)
			}
		}
		if len(kv) > 0 {
			props["Settings"] = strings.Join(kv, ", ")
		}
	}
	if trs, ok := q["Triggers"].([]any); ok {
		for _, t := range trs {
			tr, ok := t.(map[string]any)
			if !ok {
				continue
			}
			v := fmtNum(num(tr, "Calls")) + " calls"
			if tm, ok := tr["Time"].(float64); ok {
				v += ", " + fmtMs(tm)
			}
			props["Trigger "+str(tr, "Trigger Name")] = v
		}
	}
	if jit, ok := q["JIT"].(map[string]any); ok {
		v := fmtNum(num(jit, "Functions")) + " functions"
		if tm, ok := jit["Timing"].(map[string]any); ok {
			if total, ok := tm["Total"].(float64); ok {
				v += ", " + fmtMs(total)
			}
		}
		props["JIT"] = v
	}
}

func opName(p map[string]any) string {
	op := str(p, "Node Type")
	switch op {
	case "Aggregate":
		switch str(p, "Strategy") {
		case "Sorted":
			op = "GroupAggregate"
		case "Hashed":
			op = "HashAggregate"
		case "Mixed":
			op = "MixedAggregate"
		}
		switch str(p, "Partial Mode") {
		case "Partial":
			op = "Partial " + op
		case "Finalize":
			op = "Finalize " + op
		}
	case "SetOp":
		if str(p, "Strategy") == "Hashed" {
			op = "HashSetOp"
		}
		if c := str(p, "Command"); c != "" {
			op += " " + c
		}
	case "Hash Join", "Merge Join", "Nested Loop":
		if jt := str(p, "Join Type"); jt != "" && jt != "Inner" {
			op = strings.TrimSuffix(op, " Join") + " " + jt + " Join"
		}
	case "ModifyTable":
		if o := str(p, "Operation"); o != "" {
			op = o
		}
	case "Custom Scan":
		if cp := str(p, "Custom Plan Provider"); cp != "" {
			op += " (" + cp + ")"
		}
	}
	if str(p, "Scan Direction") == "Backward" {
		op += " Backward"
	}
	if b, _ := p["Parallel Aware"].(bool); b {
		op = "Parallel " + op
	}
	if b, _ := p["Async Capable"].(bool); b {
		op = "Async " + op
	}
	return op
}

// target names what a node reads: "schema.table alias" (the alias only when
// it differs), a CTE or function, or the index of a bitmap index scan.
func target(p map[string]any) string {
	alias := str(p, "Alias")
	name, bare := "", ""
	switch {
	case str(p, "Relation Name") != "":
		bare = str(p, "Relation Name")
		name = qualify(str(p, "Schema"), bare)
	case str(p, "CTE Name") != "":
		bare = str(p, "CTE Name")
		name = bare
	case str(p, "Function Name") != "":
		bare = str(p, "Function Name")
		name = qualify(str(p, "Schema"), bare)
	case str(p, "Index Name") != "":
		return str(p, "Index Name")
	default:
		return alias
	}
	if alias != "" && alias != bare {
		name += " " + alias
	}
	return name
}

func qualify(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

func bufferKey(k string) bool {
	return strings.HasSuffix(k, " Blocks") &&
		(strings.HasPrefix(k, "Shared ") || strings.HasPrefix(k, "Local ") || strings.HasPrefix(k, "Temp "))
}

// buffers formats block counts as EXPLAIN's text format does, skipping zeros:
// "shared hit=12 read=3, temp written=8".
func buffers(m map[string]any) string {
	var parts []string
	for _, g := range []struct {
		name  string
		kinds []string
	}{
		{"Shared", []string{"Hit", "Read", "Dirtied", "Written"}},
		{"Local", []string{"Hit", "Read", "Dirtied", "Written"}},
		{"Temp", []string{"Read", "Written"}},
	} {
		var f []string
		for _, k := range g.kinds {
			if v := num(m, g.name+" "+k+" Blocks"); v > 0 {
				f = append(f, strings.ToLower(k)+"="+fmtNum(v))
			}
		}
		if len(f) > 0 {
			parts = append(parts, strings.ToLower(g.name)+" "+strings.Join(f, " "))
		}
	}
	return strings.Join(parts, ", ")
}

// changed formats a count that the executor may have grown from its planned
// value: "2048 (originally 1024)".
func changed(p map[string]any, key, orig string) string {
	v, ok := p[key].(float64)
	if !ok {
		return ""
	}
	s := fmtNum(v)
	if o, ok := p[orig].(float64); ok && o != v {
		s += " (originally " + fmtNum(o) + ")"
	}
	return s
}

// propValue formats scalars and lists of scalars; objects are skipped.
func propValue(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case float64:
		return fmtNum(v), true
	case bool:
		return strconv.FormatBool(v), true
	case []any:
		parts := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ", "), len(parts) > 0
	}
	return "", false
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) float64 {
	f, _ := m[k].(float64)
	return f
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func fmtMs(f float64) string { return fmtNum(math.Round(f*1000)/1000) + " ms" }
