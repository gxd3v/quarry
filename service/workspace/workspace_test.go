package workspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"uuid"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	ws "github.com/gxd3v/quarry/internal/workspace"
)

type env struct {
	s        *Service
	store    *ws.Store
	dir      string // a directory for user files
	settings string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, err := ws.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "config", "settings.json")
	return &env{s: New(store, settings), store: store, dir: t.TempDir(), settings: settings}
}

func req[T any](m *T) *connect.Request[T] { return connect.NewRequest(m) }

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if got := connect.CodeOf(err); got != code {
		t.Errorf("code = %v (%v), want %v", got, err, code)
	}
}

var ctx = context.Background()

func TestFiles(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "q.sql")
	os.WriteFile(p, []byte("\xef\xbb\xbfselect 1;\r\nselect 2;\r\n"), 0o644)

	res, err := e.s.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: p}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.Text != "select 1;\nselect 2;\n" || res.Msg.Binding != nil || res.Msg.Scratch {
		t.Errorf("ReadFile = %v", res.Msg)
	}

	// Saving keeps the BOM and CRLF line endings.
	if _, err := e.s.WriteFile(ctx, req(&quarryv1.WriteFileRequest{Path: p, Text: "select 3;\n"})); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "\xef\xbb\xbfselect 3;\r\n" {
		t.Errorf("written = %q", b)
	}

	id := uuid.New().String()
	bind := &quarryv1.Binding{ConnId: id, Scope: &quarryv1.Scope{Database: "app", Schema: "sales"}}
	if _, err := e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: p, Binding: bind})); err != nil {
		t.Fatal(err)
	}
	res, _ = e.s.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: p}))
	if !proto.Equal(res.Msg.Binding, bind) {
		t.Errorf("binding = %v, want %v", res.Msg.Binding, bind)
	}

	// A binding without scope reports none; an empty conn_id unbinds.
	bind = &quarryv1.Binding{ConnId: id}
	e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: p, Binding: bind}))
	res, _ = e.s.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: p}))
	if !proto.Equal(res.Msg.Binding, bind) {
		t.Errorf("binding = %v, want %v", res.Msg.Binding, bind)
	}
	for _, b := range []*quarryv1.Binding{{}, nil} {
		e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: p, Binding: bind}))
		if _, err := e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: p, Binding: b})); err != nil {
			t.Fatal(err)
		}
		if res, _ = e.s.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: p})); res.Msg.Binding != nil {
			t.Errorf("Bind(%v) did not unbind", b)
		}
	}

	_, err = e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: p, Binding: &quarryv1.Binding{ConnId: "pg"}}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestFileErrors(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "x.bin")
	os.WriteFile(bin, []byte("MZ\x00"), 0o644)
	big := filepath.Join(e.dir, "big.sql")
	os.WriteFile(big, make([]byte, ws.MaxFileSize+1), 0o644)

	for _, tt := range []struct {
		path string
		code connect.Code
	}{
		{bin, connect.CodeInvalidArgument},
		{big, connect.CodeInvalidArgument},
		{filepath.Join(e.dir, "missing.sql"), connect.CodeNotFound},
		{"", connect.CodeInvalidArgument},
		{"relative.sql", connect.CodeInvalidArgument},
	} {
		_, err := e.s.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: tt.path}))
		wantCode(t, err, tt.code)
	}
	_, err := e.s.WriteFile(ctx, req(&quarryv1.WriteFileRequest{Path: "q.sql", Text: "x"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: ""}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestScratches(t *testing.T) {
	e := newEnv(t)
	var paths []string
	for _, ext := range []string{"", ".js", "txt"} {
		res, err := e.s.NewScratch(ctx, req(&quarryv1.NewScratchRequest{Ext: ext}))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, res.Msg.Path)
	}
	var names []string
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	if got := strings.Join(names, " "); got != "scratch-1.sql scratch-2.js scratch-3.txt" {
		t.Errorf("scratches = %s", got)
	}
	for _, bad := range []string{"../x", `a\b`, "."} {
		_, err := e.s.NewScratch(ctx, req(&quarryv1.NewScratchRequest{Ext: bad}))
		wantCode(t, err, connect.CodeInvalidArgument)
	}

	old := time.Now().Add(-time.Hour)
	os.Chtimes(paths[1], old, old)
	os.Chtimes(paths[2], old.Add(time.Minute), old.Add(time.Minute))
	os.WriteFile(paths[0], []byte("select 1;"), 0o600)
	list, err := e.s.ListScratches(ctx, req(&quarryv1.ListScratchesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(list.Msg.Files); n != 3 {
		t.Fatalf("%d scratches", n)
	}
	f := list.Msg.Files[0]
	if f.Path != paths[0] || f.Name != "scratch-1.sql" || f.Size != 9 || time.Since(f.Modified.AsTime()) > time.Minute {
		t.Errorf("first = %v", f)
	}
	if list.Msg.Files[2].Name != "scratch-2.js" || !list.Msg.Files[2].Modified.AsTime().Equal(old) {
		t.Errorf("last = %v", list.Msg.Files[2])
	}

	res, _ := e.s.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: paths[0]}))
	if !res.Msg.Scratch || res.Msg.Text != "select 1;" {
		t.Errorf("ReadFile(scratch) = %v", res.Msg)
	}

	e.s.Bind(ctx, req(&quarryv1.BindRequest{Path: paths[0], Binding: &quarryv1.Binding{ConnId: uuid.New().String()}}))
	if _, err := e.s.DeleteScratch(ctx, req(&quarryv1.DeleteScratchRequest{Path: paths[0]})); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.store.Binding(paths[0]); ok {
		t.Error("binding kept")
	}
	_, err = e.s.DeleteScratch(ctx, req(&quarryv1.DeleteScratchRequest{Path: paths[0]}))
	wantCode(t, err, connect.CodeNotFound)

	other := filepath.Join(e.dir, "keep.sql")
	os.WriteFile(other, nil, 0o644)
	_, err = e.s.DeleteScratch(ctx, req(&quarryv1.DeleteScratchRequest{Path: other}))
	wantCode(t, err, connect.CodeInvalidArgument)
	if _, err := os.Stat(other); err != nil {
		t.Error("non-scratch file deleted")
	}
}

func TestSession(t *testing.T) {
	e := newEnv(t)
	res, err := e.s.LoadSession(ctx, req(&quarryv1.LoadSessionRequest{}))
	if err != nil || len(res.Msg.Session.Tabs) != 0 || res.Msg.Session.LayoutJson != "" {
		t.Fatalf("empty LoadSession = %v, %v", res, err)
	}

	want := &quarryv1.Session{
		Tabs: []*quarryv1.TabState{
			{Path: filepath.Join(e.dir, "q.sql"), Title: "q.sql", ConnId: uuid.New().String(), Scope: &quarryv1.Scope{Database: "app", Schema: "s"}, Cursor: 7},
			{Title: "untitled-1", Unsaved: "select 'x';", Dirty: true},
			{Title: "conn only", ConnId: uuid.New().String()},
		},
		Active:     1,
		LayoutJson: `{"side":240,"results":{"open":true}}`,
	}
	if _, err := e.s.SaveSession(ctx, req(&quarryv1.SaveSessionRequest{Session: want})); err != nil {
		t.Fatal(err)
	}
	store, _ := ws.Open(e.store.Dir())
	res, err = New(store, e.settings).LoadSession(ctx, req(&quarryv1.LoadSessionRequest{}))
	if err != nil || !proto.Equal(res.Msg.Session, want) {
		t.Errorf("LoadSession = %v, %v\nwant %v", res.Msg.Session, err, want)
	}

	bad := proto.Clone(want).(*quarryv1.Session)
	bad.Tabs[0].ConnId = "nope"
	_, err = e.s.SaveSession(ctx, req(&quarryv1.SaveSessionRequest{Session: bad}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// A nil session clears it.
	if _, err := e.s.SaveSession(ctx, req(&quarryv1.SaveSessionRequest{})); err != nil {
		t.Fatal(err)
	}
	res, _ = e.s.LoadSession(ctx, req(&quarryv1.LoadSessionRequest{}))
	if len(res.Msg.Session.Tabs) != 0 {
		t.Errorf("after nil save: %v", res.Msg.Session)
	}
}

func TestHistory(t *testing.T) {
	e := newEnv(t)
	conn, other := uuid.New(), uuid.New()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, text := range []string{"select 1", "SELECT name from t", "update t set x = 1", "select 2"} {
		err := e.store.AppendHistory(ws.Entry{
			At: base.Add(time.Duration(i) * time.Minute), Conn: conn, Scope: driver.Scope{Database: "app"},
			Text: text, Duration: 1500 * time.Microsecond, Rows: int64(i), Err: map[bool]string{true: "boom"}[i == 2],
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	e.store.AppendHistory(ws.Entry{Conn: other, Text: "select other"})

	res, err := e.s.History(ctx, req(&quarryv1.HistoryRequest{ConnId: conn.String()}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Msg.Entries); n != 4 {
		t.Fatalf("%d entries", n)
	}
	got := res.Msg.Entries[1]
	want := &quarryv1.HistoryEntry{
		At: got.At, ConnId: conn.String(), Scope: &quarryv1.Scope{Database: "app"},
		Text: "update t set x = 1", DurationMs: 1, Rows: 2, Error: "boom",
	}
	if !proto.Equal(got, want) || !got.At.AsTime().Equal(base.Add(2*time.Minute)) {
		t.Errorf("entry = %v\nwant %v", got, want)
	}

	res, _ = e.s.History(ctx, req(&quarryv1.HistoryRequest{ConnId: conn.String(), Query: "select", Limit: 2}))
	if len(res.Msg.Entries) != 2 || res.Msg.Entries[0].Text != "select 2" || res.Msg.Entries[1].Text != "SELECT name from t" {
		t.Errorf("filtered = %v", res.Msg.Entries)
	}

	for _, r := range []*quarryv1.HistoryRequest{{ConnId: "x"}, {}, {ConnId: conn.String(), Limit: -1}} {
		_, err := e.s.History(ctx, req(r))
		wantCode(t, err, connect.CodeInvalidArgument)
	}

	if _, err := e.s.ClearHistory(ctx, req(&quarryv1.ClearHistoryRequest{ConnId: conn.String()})); err != nil {
		t.Fatal(err)
	}
	res, _ = e.s.History(ctx, req(&quarryv1.HistoryRequest{ConnId: conn.String()}))
	if len(res.Msg.Entries) != 0 {
		t.Errorf("after clear: %v", res.Msg.Entries)
	}
	res, _ = e.s.History(ctx, req(&quarryv1.HistoryRequest{ConnId: other.String()}))
	if len(res.Msg.Entries) != 1 {
		t.Errorf("other connection's history: %v", res.Msg.Entries)
	}
	_, err = e.s.ClearHistory(ctx, req(&quarryv1.ClearHistoryRequest{ConnId: "x"}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestHistoryDefaultLimit(t *testing.T) {
	e := newEnv(t)
	conn := uuid.New()
	for range defaultHistoryLimit + 5 {
		e.store.AppendHistory(ws.Entry{Conn: conn, Text: "select 1"})
	}
	res, err := e.s.History(ctx, req(&quarryv1.HistoryRequest{ConnId: conn.String()}))
	if err != nil || len(res.Msg.Entries) != defaultHistoryLimit {
		t.Errorf("got %d entries, %v; want %d", len(res.Msg.Entries), err, defaultHistoryLimit)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	res, err := e.s.GetSettings(ctx, req(&quarryv1.GetSettingsRequest{}))
	if err != nil || res.Msg.Json != "{}" {
		t.Fatalf("missing settings = %v, %v", res, err)
	}

	in := `{"theme":"dark","editor":{"fontSize":14,"tabs":[2,4]}}`
	if _, err := e.s.SaveSettings(ctx, req(&quarryv1.SaveSettingsRequest{Json: in})); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.settings)
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "{\n  \"theme\": \"dark\",\n  \"editor\": {\n    \"fontSize\": 14,\n    \"tabs\": [\n      2,\n      4\n    ]\n  }\n}\n"
	if string(b) != wantFile {
		t.Errorf("file = %q\nwant %q", b, wantFile)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(e.settings); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	}
	res, _ = e.s.GetSettings(ctx, req(&quarryv1.GetSettingsRequest{}))
	if res.Msg.Json != wantFile {
		t.Errorf("GetSettings = %q", res.Msg.Json)
	}

	for _, bad := range []string{"", "[]", `"x"`, "1", "null", "{", `{"a":1}{}`, `{"a":1,"a":2}`} {
		_, err := e.s.SaveSettings(ctx, req(&quarryv1.SaveSettingsRequest{Json: bad}))
		wantCode(t, err, connect.CodeInvalidArgument)
	}
	if b2, _ := os.ReadFile(e.settings); string(b2) != wantFile {
		t.Error("rejected save changed the file")
	}
	entries, _ := os.ReadDir(filepath.Dir(e.settings))
	if len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestSettingsHandEdited(t *testing.T) {
	e := newEnv(t)
	os.MkdirAll(filepath.Dir(e.settings), 0o700)

	// A BOM added by an editor is tolerated.
	os.WriteFile(e.settings, []byte("\xef\xbb\xbf{\"a\": 1}"), 0o600)
	res, err := e.s.GetSettings(ctx, req(&quarryv1.GetSettingsRequest{}))
	if err != nil || res.Msg.Json != `{"a": 1}` {
		t.Errorf("GetSettings = %v, %v", res, err)
	}

	// A broken file is reported, then kept aside by the next save.
	broken := `{"a": 1,}`
	os.WriteFile(e.settings, []byte(broken), 0o600)
	_, err = e.s.GetSettings(ctx, req(&quarryv1.GetSettingsRequest{}))
	wantCode(t, err, connect.CodeFailedPrecondition)
	if _, err := e.s.SaveSettings(ctx, req(&quarryv1.SaveSettingsRequest{Json: `{"b":2}`})); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(e.settings + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("corrupt copies: %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != broken {
		t.Errorf("kept aside = %q", b)
	}
	if res, err := e.s.GetSettings(ctx, req(&quarryv1.GetSettingsRequest{})); err != nil || res.Msg.Json != "{\n  \"b\": 2\n}\n" {
		t.Errorf("after save = %v, %v", res, err)
	}
}

func TestConnect(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	mux.Handle(quarryv1connect.NewWorkspaceServiceHandler(e.s))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := quarryv1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL)

	sc, err := c.NewScratch(ctx, req(&quarryv1.NewScratchRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteFile(ctx, req(&quarryv1.WriteFileRequest{Path: sc.Msg.Path, Text: "select 'ação';\n"})); err != nil {
		t.Fatal(err)
	}
	rf, err := c.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: sc.Msg.Path}))
	if err != nil || rf.Msg.Text != "select 'ação';\n" || !rf.Msg.Scratch {
		t.Errorf("ReadFile = %v, %v", rf, err)
	}

	for _, tt := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"read missing", second(c.ReadFile(ctx, req(&quarryv1.ReadFileRequest{Path: filepath.Join(e.dir, "none.sql")}))), connect.CodeNotFound},
		{"bad conn", second(c.History(ctx, req(&quarryv1.HistoryRequest{ConnId: "pg"}))), connect.CodeInvalidArgument},
		{"not scratch", second(c.DeleteScratch(ctx, req(&quarryv1.DeleteScratchRequest{Path: filepath.Join(e.dir, "x.sql")}))), connect.CodeInvalidArgument},
		{"bad settings", second(c.SaveSettings(ctx, req(&quarryv1.SaveSettingsRequest{Json: "[]"}))), connect.CodeInvalidArgument},
	} {
		if ce := new(connect.Error); !errors.As(tt.err, &ce) || ce.Code() != tt.code {
			t.Errorf("%s: err = %v, want %v", tt.name, tt.err, tt.code)
		}
	}

	if _, err := c.SaveSettings(ctx, req(&quarryv1.SaveSettingsRequest{Json: `{"k":"v"}`})); err != nil {
		t.Fatal(err)
	}
	gs, err := c.GetSettings(ctx, req(&quarryv1.GetSettingsRequest{}))
	if err != nil || gs.Msg.Json != "{\n  \"k\": \"v\"\n}\n" {
		t.Errorf("GetSettings = %v, %v", gs, err)
	}
}

func second[T any](_ T, err error) error { return err }
