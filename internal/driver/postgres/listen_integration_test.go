package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestITListen(t *testing.T) {
	c := openEd(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	got := make(chan driver.Notification, 4)
	done := make(chan error, 1)
	go func() {
		done <- c.(driver.Listener).Listen(ctx, driver.Scope{Database: edDB}, []string{"jobs", "Mixed Case"},
			func() { close(ready) }, func(n driver.Notification) { got <- n })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}

	pc := itConn(t, edDB)
	if _, err := pc.Exec(context.Background(), `SELECT pg_notify('jobs', 'olá ✓'), pg_notify('Mixed Case', ''), pg_notify('other', 'x')`); err != nil {
		t.Fatal(err)
	}
	for _, want := range []driver.Notification{{Channel: "jobs", Payload: "olá ✓"}, {Channel: "Mixed Case"}} {
		select {
		case n := <-got:
			if n.Channel != want.Channel || n.Payload != want.Payload || n.PID == 0 || n.At.IsZero() {
				t.Fatalf("got %+v, want %+v", n, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no notification on %s", want.Channel)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-got:
		t.Fatalf("unexpected %+v", n)
	default:
	}
}
