package workspace

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestSessionMissing(t *testing.T) {
	s := openStore(t)
	sess, err := s.LoadSession()
	if err != nil || !reflect.DeepEqual(sess, Session{}) {
		t.Errorf("LoadSession = %+v, %v; want zero", sess, err)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	want := Session{
		Tabs: []TabState{
			{Path: "/home/u/q.sql", Title: "q.sql", Conn: uuid.New(), Scope: driver.Scope{Database: "app", Schema: "sales"}, Cursor: 42},
			{Title: "untitled-1", Unsaved: "select 'ação' from dual;\n\t\"quoted\"\n", Dirty: true},
			{Path: "/home/u/dirty.sql", Title: "dirty.sql", Cursor: 3, Unsaved: "edited", Dirty: true},
		},
		Active: 1,
		Layout: `{"side":{"w":30,"open":true},"results":{"h":12},"note":"ação \"x\""}`,
	}
	if err := s.SaveSession(want); err != nil {
		t.Fatal(err)
	}
	noTemps(t, dir)

	s, _ = Open(dir)
	got, err := s.LoadSession()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadSession = %+v, %v\nwant %+v", got, err, want)
	}

	// Saving again (unchanged, then changed) keeps working.
	if err := s.SaveSession(want); err != nil {
		t.Fatal(err)
	}
	want.Active = 2
	want.Tabs = want.Tabs[:2]
	want.Active = 0
	if err := s.SaveSession(want); err != nil {
		t.Fatal(err)
	}
	s, _ = Open(dir)
	if got, _ := s.LoadSession(); !reflect.DeepEqual(got, want) {
		t.Errorf("after second save = %+v", got)
	}
	noTemps(t, dir)
}

func TestSessionInvalidUTF8(t *testing.T) {
	s := openStore(t)
	err := s.SaveSession(Session{Tabs: []TabState{{Unsaved: "select '\xe7';"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSession()
	if err != nil || got.Tabs[0].Unsaved != "select '�';" {
		t.Errorf("got %q, %v", got.Tabs[0].Unsaved, err)
	}
}

func TestSessionCorrupt(t *testing.T) {
	for name, content := range map[string]string{
		"truncated": `{"tabs":[{"path":"/a.sql","unsa`,
		"empty":     "",
		"wrong":     `{"session":{"tabs":3}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "session.json")
			os.WriteFile(p, []byte(content), 0o600)
			s, _ := Open(dir)
			sess, err := s.LoadSession()
			if err != nil || !reflect.DeepEqual(sess, Session{}) {
				t.Errorf("LoadSession = %+v, %v; want zero", sess, err)
			}
			matches, _ := filepath.Glob(p + ".corrupt-*")
			if len(matches) != 1 {
				t.Fatalf("quarantined files: %v", matches)
			}
			if b, _ := os.ReadFile(matches[0]); string(b) != content {
				t.Errorf("quarantined content = %q", b)
			}
			if err := s.SaveSession(Session{Layout: "{}"}); err != nil {
				t.Fatal(err)
			}
			if sess, _ := s.LoadSession(); sess.Layout != "{}" {
				t.Errorf("save after corruption failed: %+v", sess)
			}
		})
	}
}

func TestSessionLegacyLayout(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "session.json")
	old := `{"session":{"tabs":[{"title":"a"}],"side_w":30,"show_res":true},"unsaved":["x"]}`
	os.WriteFile(p, []byte(old), 0o600)
	s, _ := Open(dir)
	sess, err := s.LoadSession()
	want := Session{Tabs: []TabState{{Title: "a", Unsaved: "x"}}}
	if err != nil || !reflect.DeepEqual(sess, want) {
		t.Errorf("LoadSession = %+v, %v; want %+v", sess, err, want)
	}
	if m, _ := filepath.Glob(p + ".corrupt-*"); len(m) != 0 {
		t.Errorf("legacy session quarantined: %v", m)
	}
}

func TestSessionActiveClamped(t *testing.T) {
	s := openStore(t)
	s.SaveSession(Session{Tabs: []TabState{{Title: "a"}}, Active: 5})
	if sess, _ := s.LoadSession(); sess.Active != 0 {
		t.Errorf("Active = %d, want 0", sess.Active)
	}
}

func bigSession() Session {
	text := strings.Repeat("select * from orders where id = 42 and name = 'açaí';\n", 100<<10/55)
	var sess Session
	for i := range 20 {
		sess.Tabs = append(sess.Tabs, TabState{
			Path:    filepath.Join("/home/user/sql", "q"+string(rune('a'+i))+".sql"),
			Title:   "tab",
			Conn:    uuid.New(),
			Unsaved: text,
			Dirty:   true,
		})
	}
	return sess
}

// BenchmarkSaveSession saves 20 tabs of 100 KB of unsaved text after a
// cursor move, an edit in one tab and edits in every tab.
func BenchmarkSaveSession(b *testing.B) {
	for _, bc := range []struct {
		name   string
		edited int
	}{{"cursor", 0}, {"oneTab", 1}, {"allTabs", 20}} {
		b.Run(bc.name, func(b *testing.B) {
			s := openStore(b)
			sess := bigSession()
			texts := make([]string, len(sess.Tabs))
			for i, t := range sess.Tabs {
				texts[i] = t.Unsaved
			}
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				sess.Tabs[0].Cursor = i
				for j := range bc.edited {
					sess.Tabs[j].Unsaved = texts[j] + strconv.Itoa(i)
				}
				if err := s.SaveSession(sess); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkLoadSession(b *testing.B) {
	s := openStore(b)
	s.SaveSession(bigSession())
	for b.Loop() {
		if _, err := s.LoadSession(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSessionContentReuse(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess := Session{Tabs: []TabState{
		{Title: "a", Unsaved: "select 'a';", Dirty: true},
		{Title: "b", Unsaved: "select \"b\";\n", Dirty: true},
		{Title: "c"},
	}}
	if err := s.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	// Reordered, duplicated, edited and cleared contents.
	sess.Tabs = []TabState{
		{Title: "b", Unsaved: "select \"b\";\n", Dirty: true},
		{Title: "a", Unsaved: "select 'a'; -- edited", Dirty: true},
		{Title: "b2", Unsaved: "select \"b\";\n"},
		{Title: "c", Unsaved: ""},
	}
	if err := s.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	var anyJSON any
	if err := json.Unmarshal(b, &anyJSON); err != nil {
		t.Fatalf("session file is not valid JSON: %v\n%s", err, b)
	}
	s, _ = Open(dir)
	if got, _ := s.LoadSession(); !reflect.DeepEqual(got, sess) {
		t.Errorf("got  %+v\nwant %+v", got, sess)
	}
}
