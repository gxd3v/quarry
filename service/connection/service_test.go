package connection

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/store"
)

func newClient(t *testing.T, m *Manager) quarryv1connect.ConnectionServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewConnectionServiceHandler(NewService(m)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return quarryv1connect.NewConnectionServiceClient(srv.Client(), srv.URL)
}

func TestServiceOverConnect(t *testing.T) {
	m, d, _ := newTestManager(t, nil)
	client := newClient(t, m)
	ctx := t.Context()

	saved, err := client.SaveConnection(ctx, connect.NewRequest(&quarryv1.SaveConnectionRequest{
		Config:   &quarryv1.ConnectionConfig{Name: "Prod DB", Kind: "postgres", Host: "db", Env: "prod", AskPassword: true, Options: map[string]string{"sslmode": "require"}},
		Password: ptr("ignored"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	conn := saved.Msg.Connection
	id := conn.Config.Id
	if id == "" || conn.Config.Port != 5432 || conn.HasPassword || conn.Status.State != stDisconnected || conn.Config.Options["sslmode"] != "require" {
		t.Fatalf("saved %v", conn)
	}

	stream, err := client.WatchStatus(ctx, connect.NewRequest(&quarryv1.WatchStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() || len(stream.Msg().Statuses) != 1 || stream.Msg().Statuses[0].Id != id {
		t.Fatalf("first watch message %v, %v", stream.Msg(), stream.Err())
	}

	_, err = client.Connect(ctx, connect.NewRequest(&quarryv1.ConnectRequest{Id: id}))
	wantCode(t, err, connect.CodeUnauthenticated)
	res, err := client.Connect(ctx, connect.NewRequest(&quarryv1.ConnectRequest{Id: id, Password: ptr("pw")}))
	if err != nil || res.Msg.Status.State != stConnected {
		t.Fatalf("connect = %v, %v", res, err)
	}
	if _, pw := d.stats(); pw != "pw" {
		t.Errorf("opened with %q", pw)
	}
	waitState(t, stream, id, stConnected)

	if _, err := client.Disconnect(ctx, connect.NewRequest(&quarryv1.DisconnectRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	waitState(t, stream, id, stDisconnected)

	list, err := client.ListConnections(ctx, connect.NewRequest(&quarryv1.ListConnectionsRequest{}))
	if err != nil || len(list.Msg.Connections) != 1 || list.Msg.Connections[0].Config.Env != "prod" {
		t.Fatalf("list = %v, %v", list, err)
	}

	for _, tc := range []struct {
		name string
		call func() error
		code connect.Code
	}{
		{"connect bad id", func() error {
			_, err := client.Connect(ctx, connect.NewRequest(&quarryv1.ConnectRequest{Id: "x"}))
			return err
		}, connect.CodeInvalidArgument},
		{"connect unknown", func() error {
			_, err := client.Connect(ctx, connect.NewRequest(&quarryv1.ConnectRequest{Id: "0199d6a0-0000-7000-8000-000000000000"}))
			return err
		}, connect.CodeNotFound},
		{"save invalid", func() error {
			_, err := client.SaveConnection(ctx, connect.NewRequest(&quarryv1.SaveConnectionRequest{Config: &quarryv1.ConnectionConfig{Kind: "postgres"}}))
			return err
		}, connect.CodeInvalidArgument},
		{"save no config", func() error {
			_, err := client.SaveConnection(ctx, connect.NewRequest(&quarryv1.SaveConnectionRequest{}))
			return err
		}, connect.CodeInvalidArgument},
		{"delete unknown", func() error {
			_, err := client.DeleteConnection(ctx, connect.NewRequest(&quarryv1.DeleteConnectionRequest{Id: "0199d6a0-0000-7000-8000-000000000000"}))
			return err
		}, connect.CodeNotFound},
		{"parse bad uri", func() error {
			_, err := client.ParseURI(ctx, connect.NewRequest(&quarryv1.ParseURIRequest{Uri: "nope"}))
			return err
		}, connect.CodeInvalidArgument},
	} {
		if got := connect.CodeOf(tc.call()); got != tc.code {
			t.Errorf("%s: code %v, want %v", tc.name, got, tc.code)
		}
	}

	parsed, err := client.ParseURI(ctx, connect.NewRequest(&quarryv1.ParseURIRequest{Uri: "postgres://ann:p%40ss@h:5433/app?sslmode=disable"}))
	if err != nil {
		t.Fatal(err)
	}
	if c := parsed.Msg.Config; c.Id != "" || c.Kind != "postgres" || c.User != "ann" || c.Host != "h" || c.Port != 5433 || c.Database != "app" || c.Options["sslmode"] != "disable" || parsed.Msg.GetPassword() != "p@ss" {
		t.Errorf("parsed %v", parsed.Msg)
	}

	if _, err := client.DeleteConnection(ctx, connect.NewRequest(&quarryv1.DeleteConnectionRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if del := stream.Msg().Deleted; len(del) == 1 && del[0] == id {
			return
		}
	}
	t.Fatalf("no deletion on the stream: %v", stream.Err())
}

func waitState(t *testing.T, stream *connect.ServerStreamForClient[quarryv1.WatchStatusResponse], id string, want quarryv1.ConnectionState) {
	t.Helper()
	for stream.Receive() {
		for _, s := range stream.Msg().Statuses {
			if s.Id == id && s.State == want {
				return
			}
		}
	}
	t.Fatalf("stream ended before %s became %v: %v", id, want, stream.Err())
}

func TestListConnectionsOrder(t *testing.T) {
	secrets := store.NewMemory()
	m, _, _ := newTestManager(t, secrets)
	for _, c := range []driver.Config{
		{Name: "c", Group: "Zeta"},
		{Name: "a"},
		{Name: "e", Group: "x", Favorite: true},
		{Name: "D", Group: "alpha"},
		{Name: "b", Favorite: true},
	} {
		c.Kind = driver.Postgres
		var pw *string
		if c.Name == "D" {
			pw = ptr("pw")
		}
		mustSave(t, m, c, pw)
	}
	res, err := NewService(m).ListConnections(t.Context(), connect.NewRequest(&quarryv1.ListConnectionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range res.Msg.Connections {
		names = append(names, c.Config.Name)
		if c.HasPassword != (c.Config.Name == "D") || c.Status.State != stDisconnected {
			t.Errorf("%s: %v", c.Config.Name, c)
		}
	}
	if want := []string{"b", "e", "a", "D", "c"}; !reflect.DeepEqual(names, want) {
		t.Errorf("order %v, want %v", names, want)
	}
}

func TestListDrivers(t *testing.T) {
	path := t.TempDir() + "/c.toml"
	m, err := New(path, store.NewMemory(), WithLookup(lookupOf(
		&fakeDriver{kind: driver.Redis, lang: driver.LangRedis},
		&fakeDriver{kind: driver.Postgres, lang: driver.LangSQL},
	)))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	res, err := NewService(m).ListDrivers(t.Context(), connect.NewRequest(&quarryv1.ListDriversRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := res.Msg.Drivers
	if len(got) != 2 || got[0].Kind != "postgres" || got[0].Language != "sql" || got[0].DefaultPort != 5432 ||
		got[1].Kind != "redis" || got[1].Language != "redis" || got[1].DefaultPort != 6379 {
		t.Errorf("drivers %v", got)
	}
}

func TestTestConnection(t *testing.T) {
	m, d, _ := newTestManager(t, nil)
	svc := NewService(m)
	ctx := t.Context()
	test := func(cfg *quarryv1.ConnectionConfig, pw *string) (*quarryv1.TestConnectionResponse, error) {
		res, err := svc.TestConnection(ctx, connect.NewRequest(&quarryv1.TestConnectionRequest{Config: cfg, Password: pw}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}

	res, err := test(&quarryv1.ConnectionConfig{Kind: "postgres", Host: "db"}, ptr("typed"))
	if err != nil || res.LatencyMs < 1 {
		t.Fatalf("test = %v, %v", res, err)
	}
	if _, pw := d.stats(); pw != "typed" || !d.last().isClosed() {
		t.Errorf("opened with %q, closed %v", pw, d.last().isClosed())
	}

	a := mustSave(t, m, pgConfig("A"), ptr("stored"))
	if _, err := test(configProto(a), nil); err != nil {
		t.Fatal(err)
	}
	if _, pw := d.stats(); pw != "stored" {
		t.Errorf("opened with %q, want the stored password", pw)
	}
	wantState(t, m, a.ID, stDisconnected)

	ask := configProto(a)
	ask.AskPassword = true
	_, err = test(ask, nil)
	wantCode(t, err, connect.CodeUnauthenticated)

	d.set(func(d *fakeDriver) { d.openErr = errors.New("connection refused") })
	_, err = test(configProto(a), nil)
	wantCode(t, err, connect.CodeUnavailable)
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("message %q", err)
	}
	_, err = test(&quarryv1.ConnectionConfig{Kind: "sqlite"}, nil)
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = test(&quarryv1.ConnectionConfig{Kind: "oracle"}, nil)
	wantCode(t, err, connect.CodeFailedPrecondition)
}
