package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

type fakeLive struct{ conn driver.Conn }

func (f fakeLive) Live(string) (driver.Conn, driver.Config, error) {
	if f.conn == nil {
		return nil, driver.Config{}, service.ErrNotConnected
	}
	return f.conn, driver.Config{Kind: driver.Postgres}, nil
}

func (f fakeLive) Config(string) (driver.Config, bool) { return driver.Config{}, true }

type fakeConn struct {
	driver.Conn
	notes []driver.Notification
	err   error
}

func (c *fakeConn) Listen(ctx context.Context, _ driver.Scope, _ []string, ready func(), fn func(driver.Notification)) error {
	if c.err != nil {
		return c.err
	}
	ready()
	for _, n := range c.notes {
		fn(n)
	}
	<-ctx.Done()
	return nil
}

func TestListen(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := &fakeConn{notes: []driver.Notification{{At: at, Channel: "jobs", Payload: "p", PID: 42}}}
	ctx, cancel := context.WithCancel(t.Context())
	var got []*quarryv1.ListenResponse
	err := New(fakeLive{c}).listen(ctx, &quarryv1.ListenRequest{Channels: []string{"jobs"}}, func(r *quarryv1.ListenResponse) error {
		got = append(got, r)
		if len(got) == 2 {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].GetListening() {
		t.Fatalf("got %v", got)
	}
	if n := got[1].GetNotification(); n.Channel != "jobs" || n.Payload != "p" || n.Pid != 42 || !n.At.AsTime().Equal(at) {
		t.Fatalf("notification %v", n)
	}

	// A failed send ends the stream.
	c = &fakeConn{notes: []driver.Notification{{Channel: "a"}, {Channel: "b"}}}
	err = New(fakeLive{c}).listen(t.Context(), &quarryv1.ListenRequest{Channels: []string{"a"}}, func(r *quarryv1.ListenResponse) error {
		if r.GetNotification() != nil {
			return errors.New("gone")
		}
		return nil
	})
	if err == nil || err.Error() != "gone" {
		t.Fatalf("send failure: %v", err)
	}

	for _, tc := range []struct {
		name string
		live fakeLive
		req  *quarryv1.ListenRequest
		code connect.Code
	}{
		{"not connected", fakeLive{}, &quarryv1.ListenRequest{Channels: []string{"a"}}, connect.CodeFailedPrecondition},
		{"no listener", fakeLive{struct{ driver.Conn }{}}, &quarryv1.ListenRequest{Channels: []string{"a"}}, connect.CodeUnimplemented},
		{"no channels", fakeLive{&fakeConn{}}, &quarryv1.ListenRequest{}, connect.CodeInvalidArgument},
		{"driver error", fakeLive{&fakeConn{err: &driver.QueryError{Msg: "bad"}}}, &quarryv1.ListenRequest{Channels: []string{"a"}}, connect.CodeInvalidArgument},
	} {
		err := New(tc.live).listen(t.Context(), tc.req, func(*quarryv1.ListenResponse) error { return nil })
		if ce, ok := errors.AsType[*connect.Error](err); !ok || ce.Code() != tc.code {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.code)
		}
	}
}
