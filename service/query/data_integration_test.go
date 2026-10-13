package query

import (
	"context"
	"slices"
	"strings"
	"testing"
	"uuid"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
)

// newPGTable creates schema.items (id int primary key, label text, n int)
// with rows 1..4 in a unique schema and returns the schema and table node.
func newPGTable(t *testing.T, e *env) (string, *quarryv1.Node) {
	t.Helper()
	schema := "quarry_data_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	evs := e.runAll(t, "create schema "+schema+";\ncreate table "+schema+".items (id int primary key, label text, n int);\n"+
		"insert into "+schema+".items values (1, 'one', 30), (2, 'two', 10), (3, 'three', 20), (4, 'four', 40)", 0)
	if qe := errorEvent(evs); qe != nil {
		t.Fatal(qe)
	}
	t.Cleanup(func() { e.dropSchema(t, schema) })
	cfg, _ := e.s.live.Config("")
	return schema, &quarryv1.Node{Path: []string{cfg.Database, schema, "tables", "items"}, Kind: "table", Name: "items"}
}

func ints(evs []*quarryv1.RunEvent, col int) []int64 {
	var out []int64
	for _, ev := range evs {
		for _, r := range ev.GetRows().GetRows() {
			out = append(out, r.Values[col].GetInt())
		}
	}
	return out
}

func TestITDataView(t *testing.T) {
	e := newPGEnv(t)
	ctx := context.Background()
	schema, node := newPGTable(t, e)
	d := e.s.Data()

	evs, err := e.openData(&quarryv1.OpenDataRequest{Node: node, Filter: "n > 10", OrderBy: []string{"n DESC"}, MaxRows: 2})
	if err != nil || errorEvent(evs) != nil {
		t.Fatalf("open data: %v, %v", err, errorEvent(evs))
	}
	if got := ints(evs, 0); !slices.Equal(got, []int64{4, 1}) || !dones(evs)[0].HasMore {
		t.Fatalf("ids = %v, done = %v", got, dones(evs))
	}
	evs, err = e.fetch(t, -1)
	if got := ints(evs, 0); err != nil || !slices.Equal(got, []int64{3}) || dones(evs)[0].HasMore {
		t.Fatalf("fetch = %v, %v", got, err)
	}
	if _, err := e.openData(&quarryv1.OpenDataRequest{Node: node, OrderBy: []string{"n; drop"}}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad order: %v", err)
	}

	key, err := d.RowKey(ctx, connect.NewRequest(&quarryv1.RowKeyRequest{Node: node}))
	if err != nil || !slices.Equal(key.Msg.Columns, []string{"id"}) {
		t.Fatalf("row key = %v, %v", key, err)
	}

	text := func(s string) *quarryv1.Value { return &quarryv1.Value{Kind: &quarryv1.Value_Text{Text: s}} }
	num := func(n int64) *quarryv1.Value { return &quarryv1.Value{Kind: &quarryv1.Value_Int{Int: n}} }
	_, err = d.ApplyChanges(ctx, connect.NewRequest(&quarryv1.ApplyChangesRequest{Node: node, Changes: []*quarryv1.RowChange{
		{Op: quarryv1.ChangeOp_CHANGE_OP_INSERT, Values: map[string]*quarryv1.Value{"id": text("10"), "label": text("ten"), "n": num(100)}},
		{Op: quarryv1.ChangeOp_CHANGE_OP_UPDATE, Key: map[string]*quarryv1.Value{"id": num(1)}, Values: map[string]*quarryv1.Value{"label": text("uno"), "n": {Kind: &quarryv1.Value_Null{Null: true}}}},
		{Op: quarryv1.ChangeOp_CHANGE_OP_DELETE, Key: map[string]*quarryv1.Value{"id": num(2)}},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	evs = e.runAll(t, "select id, label, coalesce(n, -1) from "+schema+".items order by id", 0)
	var got []string
	for _, ev := range evs {
		for _, r := range ev.GetRows().GetRows() {
			got = append(got, r.Values[1].GetText())
		}
	}
	if !slices.Equal(ints(evs, 0), []int64{1, 3, 4, 10}) || !slices.Equal(got, []string{"uno", "three", "four", "ten"}) || !slices.Equal(ints(evs, 2), []int64{-1, 20, 40, 100}) {
		t.Fatalf("after apply: %v %v %v", ints(evs, 0), got, ints(evs, 2))
	}

	// A failing change rolls back the whole batch.
	_, err = d.ApplyChanges(ctx, connect.NewRequest(&quarryv1.ApplyChangesRequest{Node: node, Changes: []*quarryv1.RowChange{
		{Op: quarryv1.ChangeOp_CHANGE_OP_DELETE, Key: map[string]*quarryv1.Value{"id": num(3)}},
		{Op: quarryv1.ChangeOp_CHANGE_OP_INSERT, Values: map[string]*quarryv1.Value{"id": text("4")}},
	}}))
	if code(err) != connect.CodeInvalidArgument || !strings.Contains(message(err), "duplicate key") {
		t.Fatalf("duplicate insert: %v", err)
	}
	if evs := e.runAll(t, "select count(*) from "+schema+".items", 0); ints(evs, 0)[0] != 4 {
		t.Fatalf("rows after rollback = %v", ints(evs, 0))
	}
}

func TestITGuardsAndParams(t *testing.T) {
	e := newPGEnv(t)
	schema, _ := newPGTable(t, e)
	table := schema + ".items"

	evs, err := e.runReq(&quarryv1.RunRequest{Script: "select $1::int + :x as v", Params: map[string]string{"1": "2", "x": "3"}})
	if err != nil || errorEvent(evs) != nil || ints(evs, 0)[0] != 5 {
		t.Fatalf("params run = %v, %v, %v", kinds(evs), err, errorEvent(evs))
	}
	evs, err = e.runReq(&quarryv1.RunRequest{Script: "select label from " + table + " where label = :l", Params: map[string]string{"l": "it's"}})
	if err != nil || errorEvent(evs) != nil || len(batches(evs)) != 0 {
		t.Fatalf("quoted param = %v, %v, %v", kinds(evs), err, errorEvent(evs))
	}
	if _, err := e.runReq(&quarryv1.RunRequest{Script: "select :missing"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing param: %v", err)
	}

	res := e.inspect(t, e.tab, "delete from "+table+";\ndrop table "+table)
	if len(res.Statements) != 2 || res.Statements[0].Risk != "unbounded_write" || res.Statements[1].Risk != "destructive" || !res.NeedsConfirmation {
		t.Fatalf("inspect = %v", res)
	}
	if _, err := e.runReq(&quarryv1.RunRequest{Script: "delete from " + table}); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed delete: %v", err)
	}
	if evs := e.runAll(t, "select count(*) from "+table, 0); ints(evs, 0)[0] != 4 {
		t.Fatalf("rows after refused delete = %v", ints(evs, 0))
	}
	evs, err = e.runReq(&quarryv1.RunRequest{Script: "delete from " + table, Confirmed: true})
	if err != nil || dones(evs)[0].Affected != 4 {
		t.Fatalf("confirmed delete = %v, %v", kinds(evs), err)
	}
}
