package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/service"
)

func start(t *testing.T) *Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewDevServiceHandler(service.Dev{}, HandlerOptions()...))
	s, err := Start(mux)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func client(s *Server, token string) quarryv1connect.DevServiceClient {
	hc := &http.Client{Transport: bearer(token)}
	return quarryv1connect.NewDevServiceClient(hc, s.URL)
}

func TestStreamArrivesProgressively(t *testing.T) {
	s := start(t)
	req := connect.NewRequest(&quarryv1.StreamRowsRequest{Rows: 4200, Columns: 3, DelayMs: 150})
	begin := time.Now()
	stream, err := client(s, s.Token).StreamRows(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var first time.Duration
	rows, msgs := 0, 0
	for stream.Receive() {
		if msgs == 0 {
			first = time.Since(begin)
			if got := len(stream.Msg().Columns); got != 3 {
				t.Fatalf("first message has %d columns, want 3", got)
			}
		} else if len(stream.Msg().Columns) != 0 {
			t.Fatal("columns repeated after the first message")
		}
		msgs++
		rows += len(stream.Msg().Rows)
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	total := time.Since(begin)
	if rows != 4200 || msgs != 3 {
		t.Fatalf("got %d rows in %d messages, want 4200 in 3", rows, msgs)
	}
	if first > total/2 {
		t.Fatalf("first batch after %v of %v: response was buffered", first, total)
	}
}

func TestEmptyResultSendsColumns(t *testing.T) {
	s := start(t)
	stream, err := client(s, s.Token).StreamRows(t.Context(), connect.NewRequest(&quarryv1.StreamRowsRequest{Columns: 2}))
	if err != nil {
		t.Fatal(err)
	}
	msgs := 0
	for stream.Receive() {
		msgs++
		if len(stream.Msg().Columns) != 2 || len(stream.Msg().Rows) != 0 {
			t.Fatalf("unexpected message %v", stream.Msg())
		}
	}
	if err := stream.Err(); err != nil || msgs != 1 {
		t.Fatalf("msgs=%d err=%v", msgs, err)
	}
}

func TestRejectsBadToken(t *testing.T) {
	s := start(t)
	stream, err := client(s, "wrong").StreamRows(t.Context(), connect.NewRequest(&quarryv1.StreamRowsRequest{Rows: 1, Columns: 1}))
	if err == nil {
		for stream.Receive() {
			t.Fatal("received a message with a bad token")
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
}

func TestCancelStopsStream(t *testing.T) {
	s := start(t)
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := client(s, s.Token).StreamRows(ctx, connect.NewRequest(&quarryv1.StreamRowsRequest{Rows: 1_000_000, Columns: 1, DelayMs: 50}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	cancel()
	for stream.Receive() {
	}
	if connect.CodeOf(stream.Err()) != connect.CodeCanceled {
		t.Fatalf("got %v, want canceled", stream.Err())
	}
}

func TestCORS(t *testing.T) {
	s := start(t)
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"wails://wails", http.StatusNoContent},
		{"http://wails.localhost", http.StatusNoContent},
		{"http://localhost:34115", http.StatusNoContent},
		{"https://evil.example", http.StatusForbidden},
		{"null", http.StatusForbidden},
	} {
		req, _ := http.NewRequest(http.MethodOptions, s.URL+quarryv1connect.DevServiceStreamRowsProcedure, nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.origin, res.StatusCode, tc.want)
		}
		if tc.want == http.StatusNoContent && res.Header.Get("Access-Control-Allow-Origin") != tc.origin {
			t.Errorf("%s: allow-origin %q", tc.origin, res.Header.Get("Access-Control-Allow-Origin"))
		}
	}
}
