package query

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
)

var tableNode = &quarryv1.Node{Path: []string{"db", "s", "tables", "t"}, Kind: "table", Name: "t"}

func newDataEnv(t *testing.T, stmts map[string]fakeStmt) (*env, *fakeDataConn) {
	t.Helper()
	e := newEnv(t, stmts)
	dc := &fakeDataConn{fakeConn: e.conn}
	e.live.add("pg", driver.Postgres, dc)
	return e, dc
}

func (e *env) openData(req *quarryv1.OpenDataRequest) ([]*quarryv1.RunEvent, error) {
	if req.TabId == "" {
		req.TabId = e.tab
	}
	var evs []*quarryv1.RunEvent
	err := e.s.openData(context.Background(), req, func(ev *quarryv1.RunEvent) error { evs = append(evs, ev); return nil })
	return evs, err
}

func TestOpenData(t *testing.T) {
	const q = "select * from db.s.tables.t where (x > 1) order by y DESC, z"
	e, _ := newDataEnv(t, map[string]fakeStmt{q: {cols: 2, rows: 30}})
	evs, err := e.openData(&quarryv1.OpenDataRequest{Node: tableNode, Filter: "x > 1", OrderBy: []string{"y DESC", "z"}, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(evs); !slices.Equal(got, []string{"started", "statement", "columns", "rows", "done", "finished"}) {
		t.Fatalf("events = %v", got)
	}
	if r := evs[1].GetStatement().Range; r.Start != 0 || r.End != int32(len(q)) {
		t.Fatalf("range = %v", r)
	}
	if d := dones(evs)[0]; d.Rows != 10 || !d.HasMore {
		t.Fatalf("done = %v", d)
	}
	if h := e.hist.all(); len(h) != 1 || h[0].Text != q || h[0].Rows != 10 {
		t.Fatalf("history = %+v", h)
	}
	evs, err = e.fetch(t, -1)
	if err != nil || !slices.Equal(batches(evs), []int{20}) || dones(evs)[0].Rows != 30 || dones(evs)[0].HasMore {
		t.Fatalf("fetch = %v, %v", kinds(evs), err)
	}

	// A filter that smuggles in a write is refused before running.
	ran := len(e.conn.sessionsRan())
	_, err = e.openData(&quarryv1.OpenDataRequest{Node: tableNode, Filter: "true); delete from t; select (1"})
	if code(err) != connect.CodeFailedPrecondition || len(e.conn.sessionsRan()) != ran {
		t.Fatalf("injected filter: %v", err)
	}
	if _, err := e.openData(&quarryv1.OpenDataRequest{}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("no node: %v", err)
	}
	if _, err := e.openData(&quarryv1.OpenDataRequest{TabId: "nope", Node: tableNode}); code(err) != connect.CodeNotFound {
		t.Fatalf("unknown tab: %v", err)
	}

	plain := newEnv(t, nil)
	if _, err := plain.openData(&quarryv1.OpenDataRequest{Node: tableNode}); code(err) != connect.CodeUnimplemented {
		t.Fatalf("no data view: %v", err)
	}
}

func TestOpenDataBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _ := newDataEnv(t, map[string]fakeStmt{"select slow": {cols: 1, rows: 10, blockAt: 1}})
		r := e.runBG(context.Background(), "select slow")
		synctest.Wait()
		if _, err := e.openData(&quarryv1.OpenDataRequest{Node: tableNode}); code(err) != connect.CodeFailedPrecondition || !errors.Is(err, errBusy) {
			t.Fatalf("busy tab: %v", err)
		}
		e.cancel(t)
		<-r.done
	})
}

func TestRowKey(t *testing.T) {
	ctx := context.Background()
	e, dc := newDataEnv(t, nil)
	dc.key = []string{"id", "k"}
	d := e.s.Data()
	res, err := d.RowKey(ctx, connect.NewRequest(&quarryv1.RowKeyRequest{ConnId: "pg", Node: tableNode}))
	if err != nil || !slices.Equal(res.Msg.Columns, []string{"id", "k"}) || !slices.Equal(dc.target.Path, tableNode.Path) {
		t.Fatalf("row key = %v, %v", res, err)
	}
	dc.keyErr = &driver.QueryError{Msg: "relation does not exist"}
	if _, err := d.RowKey(ctx, connect.NewRequest(&quarryv1.RowKeyRequest{ConnId: "pg", Node: tableNode})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("row key error: %v", err)
	}

	e.live.add("plain", driver.Postgres, newFakeConn(nil))
	res, err = d.RowKey(ctx, connect.NewRequest(&quarryv1.RowKeyRequest{ConnId: "plain", Node: tableNode}))
	if err != nil || len(res.Msg.Columns) != 0 {
		t.Fatalf("not editable = %v, %v", res, err)
	}
	e.live.add("off", driver.Postgres, nil)
	if _, err := d.RowKey(ctx, connect.NewRequest(&quarryv1.RowKeyRequest{ConnId: "off"})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("not connected: %v", err)
	}
}

func TestApplyChanges(t *testing.T) {
	ctx := context.Background()
	e, dc := newDataEnv(t, nil)
	d := e.s.Data()
	text := func(s string) *quarryv1.Value { return &quarryv1.Value{Kind: &quarryv1.Value_Text{Text: s}} }
	num := func(n int64) *quarryv1.Value { return &quarryv1.Value{Kind: &quarryv1.Value_Int{Int: n}} }
	null := &quarryv1.Value{Kind: &quarryv1.Value_Null{Null: true}}
	req := func(confirmed bool, changes ...*quarryv1.RowChange) *connect.Request[quarryv1.ApplyChangesRequest] {
		return connect.NewRequest(&quarryv1.ApplyChangesRequest{ConnId: "pg", Node: tableNode, Changes: changes, Confirmed: confirmed})
	}
	changes := []*quarryv1.RowChange{
		{Op: quarryv1.ChangeOp_CHANGE_OP_INSERT, Values: map[string]*quarryv1.Value{"name": text("a"), "n": num(5), "note": null}},
		{Op: quarryv1.ChangeOp_CHANGE_OP_UPDATE, Key: map[string]*quarryv1.Value{"id": num(1)}, Values: map[string]*quarryv1.Value{"name": text("b")}},
		{Op: quarryv1.ChangeOp_CHANGE_OP_DELETE, Key: map[string]*quarryv1.Value{"id": num(2)}},
	}
	if _, err := d.ApplyChanges(ctx, req(false, changes...)); err != nil {
		t.Fatal(err)
	}
	want := []driver.RowChange{
		{Op: driver.OpInsert, Values: map[string]any{"name": "a", "n": int64(5), "note": nil}},
		{Op: driver.OpUpdate, Key: map[string]any{"id": int64(1)}, Values: map[string]any{"name": "b"}},
		{Op: driver.OpDelete, Key: map[string]any{"id": int64(2)}},
	}
	if len(dc.applied) != 1 || !reflect.DeepEqual(dc.applied[0], want) || !slices.Equal(dc.target.Path, tableNode.Path) {
		t.Fatalf("applied = %#v", dc.applied)
	}

	if _, err := d.ApplyChanges(ctx, req(false, &quarryv1.RowChange{Values: map[string]*quarryv1.Value{"a": num(1)}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("unspecified op: %v", err)
	}
	dc.applyErr = &driver.QueryError{Msg: "updating row id=1: value too long", Detail: "limit 20"}
	if _, err := d.ApplyChanges(ctx, req(false, changes...)); code(err) != connect.CodeInvalidArgument || message(err) != "updating row id=1: value too long: limit 20" {
		t.Fatalf("query error: %v", err)
	}
	dc.applyErr = errors.New("connection reset")
	if _, err := d.ApplyChanges(ctx, req(false, changes...)); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("other error: %v", err)
	}
	dc.applyErr = nil

	e.live.configure("pg", func(c *driver.Config) { c.Env = "prod" })
	if _, err := d.ApplyChanges(ctx, req(false, changes...)); code(err) != connect.CodeFailedPrecondition || message(err) != "confirmation required: saving 3 changes to a prod connection" {
		t.Fatalf("prod unconfirmed: %v", err)
	}
	if _, err := d.ApplyChanges(ctx, req(true, changes...)); err != nil || len(dc.applied) != 2 {
		t.Fatalf("prod confirmed: %v", err)
	}
	e.live.configure("pg", func(c *driver.Config) { c.ReadOnly = true })
	if _, err := d.ApplyChanges(ctx, req(true, changes...)); code(err) != connect.CodeFailedPrecondition || message(err) != "connection is read-only" {
		t.Fatalf("read-only: %v", err)
	}
	if len(dc.applied) != 2 {
		t.Fatal("refused changes were applied")
	}

	e.live.add("plain", driver.Postgres, newFakeConn(nil))
	if _, err := d.ApplyChanges(ctx, connect.NewRequest(&quarryv1.ApplyChangesRequest{ConnId: "plain"})); code(err) != connect.CodeUnimplemented {
		t.Fatalf("not editable: %v", err)
	}
}

func TestDataConnect(t *testing.T) {
	const q = "select * from db.s.tables.t"
	e, dc := newDataEnv(t, map[string]fakeStmt{q: {cols: 1, rows: 3}})
	dc.key = []string{"id"}
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewDataServiceHandler(e.s.Data()))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := quarryv1connect.NewDataServiceClient(srv.Client(), srv.URL)

	stream, err := c.OpenData(context.Background(), connect.NewRequest(&quarryv1.OpenDataRequest{TabId: e.tab, Node: tableNode}))
	if err != nil {
		t.Fatal(err)
	}
	if evs := collect(t, stream); !slices.Equal(batches(evs), []int{3}) {
		t.Fatalf("events = %v", kinds(evs))
	}
	res, err := c.RowKey(context.Background(), connect.NewRequest(&quarryv1.RowKeyRequest{ConnId: "pg", Node: tableNode}))
	if err != nil || !slices.Equal(res.Msg.Columns, []string{"id"}) {
		t.Fatalf("row key = %v, %v", res, err)
	}
}

type fakeTTLConn struct {
	*fakeConn
	ttl time.Duration
}

func (c *fakeTTLConn) SetKeyTTL(_ context.Context, _ *driver.Node, ttl time.Duration) error {
	c.ttl = ttl
	return nil
}

func TestSetTTL(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	tc := &fakeTTLConn{fakeConn: e.conn}
	e.live.add("rd", driver.Redis, tc)
	d := e.s.Data()
	req := func(id string, ms int64, confirmed bool) *connect.Request[quarryv1.SetTTLRequest] {
		return connect.NewRequest(&quarryv1.SetTTLRequest{ConnId: id, Node: tableNode, TtlMs: ms, Confirmed: confirmed})
	}
	if _, err := d.SetTTL(ctx, req("rd", 1500, false)); err != nil || tc.ttl != 1500*time.Millisecond {
		t.Fatalf("set: %v, ttl %v", err, tc.ttl)
	}
	if _, err := d.SetTTL(ctx, req("pg", 1, false)); code(err) != connect.CodeUnimplemented {
		t.Fatalf("pg: %v", err)
	}
	e.live.configure("rd", func(c *driver.Config) { c.Env = "prod" })
	if _, err := d.SetTTL(ctx, req("rd", 0, false)); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("prod unconfirmed: %v", err)
	}
	if _, err := d.SetTTL(ctx, req("rd", 0, true)); err != nil || tc.ttl != 0 {
		t.Fatalf("prod confirmed: %v", err)
	}
	e.live.configure("rd", func(c *driver.Config) { c.ReadOnly = true })
	if _, err := d.SetTTL(ctx, req("rd", 0, true)); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("read-only: %v", err)
	}
}

type fakeCountConn struct {
	*fakeConn
	filter string
}

func (c *fakeCountConn) CountRows(_ context.Context, _ *driver.Node, filter string) (int64, error) {
	c.filter = filter
	if filter == "bad" {
		return 0, &driver.QueryError{Msg: "syntax error"}
	}
	return 42, nil
}

func TestCountRows(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	cc := &fakeCountConn{fakeConn: e.conn}
	e.live.add("cnt", driver.Postgres, cc)
	d := e.s.Data()
	req := func(id, filter string) *connect.Request[quarryv1.CountRowsRequest] {
		return connect.NewRequest(&quarryv1.CountRowsRequest{ConnId: id, Node: tableNode, Filter: filter})
	}
	res, err := d.CountRows(ctx, req("cnt", "x > 1"))
	if err != nil || res.Msg.Count != 42 || cc.filter != "x > 1" {
		t.Fatalf("count: %v %v, filter %q", res, err, cc.filter)
	}
	if _, err := d.CountRows(ctx, req("cnt", "bad")); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad filter: %v", err)
	}
	if _, err := d.CountRows(ctx, req("pg", "")); code(err) != connect.CodeUnimplemented {
		t.Fatalf("no counter: %v", err)
	}
}
