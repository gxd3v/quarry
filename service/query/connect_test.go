package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
)

func newClient(t *testing.T, s *Service) quarryv1connect.QueryServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewQueryServiceHandler(s))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return quarryv1connect.NewQueryServiceClient(srv.Client(), srv.URL)
}

func collect(t *testing.T, stream *connect.ServerStreamForClient[quarryv1.RunEvent]) []*quarryv1.RunEvent {
	t.Helper()
	var evs []*quarryv1.RunEvent
	for stream.Receive() {
		evs = append(evs, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestConnect(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, map[string]fakeStmt{
		"select big":  {cols: 2, rows: 3000},
		"select slow": {cols: 1, rows: 1000, blockAt: 300},
	})
	c := newClient(t, e.s)

	tab, err := c.OpenTab(ctx, connect.NewRequest(&quarryv1.OpenTabRequest{ConnId: "pg"}))
	if err != nil {
		t.Fatal(err)
	}
	if tab.Msg.Language != "sql" {
		t.Fatalf("language = %q", tab.Msg.Language)
	}
	id := tab.Msg.TabId

	stream, err := c.Run(ctx, connect.NewRequest(&quarryv1.RunRequest{TabId: id, Script: "select big", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT, MaxRows: 1000}))
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	if got := kinds(evs); !slices.Equal(got, []string{"started", "statement", "columns", "rows", "rows", "done", "finished"}) {
		t.Fatalf("events = %v", got)
	}
	if d := dones(evs)[0]; d.Rows != 1000 || !d.HasMore {
		t.Fatalf("done = %v", d)
	}

	fetch, err := c.Fetch(ctx, connect.NewRequest(&quarryv1.FetchRequest{TabId: id, MaxRows: -1}))
	if err != nil {
		t.Fatal(err)
	}
	evs = collect(t, fetch)
	if got := batches(evs); !slices.Equal(got, []int{2000}) || dones(evs)[0].Rows != 3000 || dones(evs)[0].HasMore {
		t.Fatalf("fetch = %v %v", got, dones(evs))
	}

	// Closing the client stream cancels the run and frees the tab.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err = c.Run(sctx, connect.NewRequest(&quarryv1.RunRequest{TabId: id, Script: "select slow", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT}))
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for rows < 300 && stream.Receive() {
		if r := stream.Msg().GetRows(); r != nil {
			rows += len(r.Rows)
		}
	}
	if rows != 300 {
		t.Fatalf("received %d rows before closing: %v", rows, stream.Err())
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-e.conn.unblocked:
	case <-time.After(10 * time.Second):
		t.Fatal("closing the stream did not cancel the run")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		stream, err = c.Run(ctx, connect.NewRequest(&quarryv1.RunRequest{TabId: id, Script: "select 1", Mode: quarryv1.RunMode_RUN_MODE_STATEMENT}))
		if err != nil {
			t.Fatal(err)
		}
		evs := drain(stream)
		if len(evs) > 0 {
			if f := evs[len(evs)-1].GetFinished(); f == nil || f.Canceled {
				t.Fatalf("run after close = %v", kinds(evs))
			}
			break
		}
		if connect.CodeOf(stream.Err()) != connect.CodeFailedPrecondition || time.Now().After(deadline) {
			t.Fatalf("run after close: %v", stream.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h := e.hist.all(); h[len(h)-2].Err != "canceled" {
		t.Fatalf("history = %+v", h)
	}

	if _, err := c.Cancel(ctx, connect.NewRequest(&quarryv1.CancelRequest{TabId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("cancel unknown tab: %v", err)
	}
	an, err := c.Analyze(ctx, connect.NewRequest(&quarryv1.AnalyzeRequest{TabId: id, Script: "select 1; select 2", Cursor: 12}))
	if err != nil || an.Msg.Current != 1 || len(an.Msg.Statements) != 2 {
		t.Fatalf("analyze = %v, %v", an, err)
	}
	if _, err := c.CloseTab(ctx, connect.NewRequest(&quarryv1.CloseTabRequest{TabId: id})); err != nil {
		t.Fatal(err)
	}
	if e.conn.count() != 1 || !e.conn.session(0).closed.Load() {
		t.Fatal("tab session not closed")
	}
}

// drain drains a stream, leaving its error for the caller.
func drain(stream *connect.ServerStreamForClient[quarryv1.RunEvent]) []*quarryv1.RunEvent {
	var evs []*quarryv1.RunEvent
	for stream.Receive() {
		evs = append(evs, stream.Msg())
	}
	return evs
}
