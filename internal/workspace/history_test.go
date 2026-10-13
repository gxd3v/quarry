package workspace

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

func texts(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Text
	}
	return out
}

func TestHistoryRoundTrip(t *testing.T) {
	s := openStore(t)
	conn := uuid.New()
	at := time.Date(2026, 10, 10, 12, 0, 0, 123456789, time.UTC)
	want := Entry{
		At:       at,
		Conn:     conn,
		Scope:    driver.Scope{Database: "app", Schema: "public"},
		Text:     "select *\nfrom t\twhere x = 'ação' and y = \"q\";",
		Duration: 1234567 * time.Microsecond,
		Rows:     -1,
		Err:      "ERROR: boom",
	}
	if err := s.AppendHistory(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.History(conn, "", 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("History = %v, %v", got, err)
	}
	if !got[0].At.Equal(at) {
		t.Errorf("At = %v, want %v", got[0].At, at)
	}
	got[0].At = at
	if got[0] != want {
		t.Errorf("got  %+v\nwant %+v", got[0], want)
	}

	// A zero At is stamped.
	before := time.Now()
	s.AppendHistory(Entry{Conn: conn, Text: "now"})
	got, _ = s.History(conn, "now", 1)
	if len(got) != 1 || got[0].At.Before(before.Add(-time.Second)) {
		t.Errorf("stamped entry = %+v", got)
	}
}

func TestHistoryFilterLimitOrder(t *testing.T) {
	s := openStore(t)
	a, b := uuid.New(), uuid.New()
	for i := range 10 {
		s.AppendHistory(Entry{Conn: a, Text: fmt.Sprintf("SELECT %d FROM Ação", i), Rows: int64(i)})
		if i%3 == 0 {
			s.AppendHistory(Entry{Conn: a, Text: fmt.Sprintf(`update "T" set x = %d`, i)})
		}
		s.AppendHistory(Entry{Conn: b, Text: fmt.Sprintf("other %d", i)})
	}

	got, _ := s.History(a, "", 3)
	if want := `[update "T" set x = 9 SELECT 9 FROM Ação SELECT 8 FROM Ação]`; fmt.Sprint(texts(got)) != want {
		t.Errorf("newest 3 = %v", texts(got))
	}
	if got, _ := s.History(a, "", 0); len(got) != 14 {
		t.Errorf("all = %d entries, want 14", len(got))
	}
	if got, _ := s.History(a, "", -1); len(got) != 14 {
		t.Errorf("limit -1 = %d entries, want 14", len(got))
	}
	got, _ = s.History(a, "select 1", 0)
	if fmt.Sprint(texts(got)) != "[SELECT 1 FROM Ação]" {
		t.Errorf("filter = %v", texts(got))
	}
	if got, _ := s.History(a, "AÇÃO", 0); len(got) != 10 {
		t.Errorf("non-ASCII case-insensitive filter = %d entries, want 10", len(got))
	}
	// Queries with characters that JSON escapes skip the fast screening.
	got, _ = s.History(a, `"t" SET`, 2)
	if fmt.Sprint(texts(got)) != `[update "T" set x = 9 update "T" set x = 6]` {
		t.Errorf("quoted filter = %v", texts(got))
	}
	if got, _ := s.History(a, "nothing like this", 5); len(got) != 0 {
		t.Errorf("no match = %v", texts(got))
	}
	// Filter applies to the text only.
	if got, _ := s.History(b, "app", 0); len(got) != 0 {
		t.Errorf("matched outside text: %v", texts(got))
	}
	if got, _ := s.History(b, "", 0); len(got) != 10 || got[0].Conn != b {
		t.Errorf("conn b = %d entries", len(got))
	}
	if got, err := s.History(uuid.New(), "", 0); err != nil || len(got) != 0 {
		t.Errorf("unknown conn = %v, %v", got, err)
	}
}

func TestHistoryLongLines(t *testing.T) {
	s := openStore(t)
	conn := uuid.New()
	var want []string
	for i := range 7 {
		text := fmt.Sprintf("-- %d\n", i) + strings.Repeat(fmt.Sprint(i), (i*37)<<10)
		want = append([]string{text}, want...)
		s.AppendHistory(Entry{Conn: conn, Text: text})
	}
	got, err := s.History(conn, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts(got), "|") != strings.Join(want, "|") {
		t.Errorf("lines straddling read blocks were mangled (%d entries)", len(got))
	}
}

func TestHistoryRotation(t *testing.T) {
	s := openStore(t)
	s.maxHistory = 1000
	conn := uuid.New()
	for i := range 100 {
		if err := s.AppendHistory(Entry{Conn: conn, Text: fmt.Sprintf("select %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	path := s.historyPath(conn)
	for _, p := range []string{path, path + ".1"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > s.maxHistory {
			t.Errorf("%s is %d bytes, over the limit", filepath.Base(p), fi.Size())
		}
	}
	if m, _ := filepath.Glob(path + "*"); len(m) != 2 {
		t.Errorf("history files = %v, want current and .1", m)
	}
	got, _ := s.History(conn, "", 0)
	if len(got) < 10 || len(got) >= 100 {
		t.Fatalf("%d entries survive rotation", len(got))
	}
	for i, e := range got {
		if want := fmt.Sprintf("select %03d", 99-i); e.Text != want {
			t.Fatalf("entry %d = %q, want %q (order across rotated files)", i, e.Text, want)
		}
	}
	got, _ = s.History(conn, "", 3)
	if fmt.Sprint(texts(got)) != "[select 099 select 098 select 097]" {
		t.Errorf("limited = %v", texts(got))
	}

	if err := s.ClearHistory(conn); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(path + "*"); len(m) != 0 {
		t.Errorf("files after clear: %v", m)
	}
	if got, _ := s.History(conn, "", 0); len(got) != 0 {
		t.Errorf("entries after clear: %v", texts(got))
	}
	if err := s.ClearHistory(conn); err != nil {
		t.Errorf("clearing empty history: %v", err)
	}
}

func TestHistoryCorruptLines(t *testing.T) {
	s := openStore(t)
	conn := uuid.New()
	s.AppendHistory(Entry{Conn: conn, Text: "first"})
	f, _ := os.OpenFile(s.historyPath(conn), os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString("garbage\n\n{\"at\":3}\n\x00\x01\n{\"at\":\"2026-10-10T00:00:00Z\",\"text\":\"partial")
	f.Close()
	// Restart after the crash.
	s, _ = Open(s.dir)
	if err := s.AppendHistory(Entry{Conn: conn, Text: "after crash"}); err != nil {
		t.Fatal(err)
	}
	s.AppendHistory(Entry{Conn: conn, Text: "later"})
	got, err := s.History(conn, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(texts(got)) != "[later after crash first]" {
		t.Errorf("entries = %q", texts(got))
	}
}

func TestHistoryConcurrent(t *testing.T) {
	s := openStore(t)
	s.maxHistory = 64 << 10
	conns := []uuid.UUID{uuid.New(), uuid.New()}
	const writers, per = 8, 40
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range per {
				e := Entry{Conn: conns[w%2], Text: fmt.Sprintf("w%d i%d", w, i)}
				if err := s.AppendHistory(e); err != nil {
					t.Error(err)
					return
				}
			}
		})
		wg.Go(func() {
			for range per {
				if _, err := s.History(conns[w%2], "w", 5); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()

	for _, c := range conns {
		got, _ := s.History(c, "", 0)
		if len(got) != writers/2*per {
			t.Errorf("conn %v: %d entries, want %d", c, len(got), writers/2*per)
		}
	}
}

// writeHistory fills conn's history file with about size bytes of entries.
func writeHistory(b *testing.B, s *Store, conn uuid.UUID, size int) int {
	var sb strings.Builder
	at := time.Now()
	n := 0
	for sb.Len() < size {
		line, _ := json.Marshal(entryJSON{
			At:   at.Add(time.Duration(n) * time.Second),
			DB:   "app",
			Text: fmt.Sprintf("select o.id, o.total, c.name from orders o join customers c on c.id = o.customer_id where o.id = %d", n),
			Dur:  int64(n) * 1000,
			Rows: 1,
		})
		sb.Write(line)
		sb.WriteByte('\n')
		n++
	}
	if err := os.WriteFile(s.historyPath(conn), []byte(sb.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	return n
}

func BenchmarkHistory(b *testing.B) {
	s := openStore(b)
	conn := uuid.New()
	n := writeHistory(b, s, conn, 5<<20)
	b.Logf("%d entries", n)
	for _, bc := range []struct {
		name, query string
		limit       int
	}{
		{"recent50", "", 50},
		{"filter50", "= 1", 50},       // matches spread through the file
		{"miss", "no such thing", 50}, // full scan, screened
		{"missQuoted", `"zz"`, 50},    // full scan, every line decoded
		{"all", "", 0},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.History(conn, bc.query, bc.limit); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAppendHistory(b *testing.B) {
	s := openStore(b)
	e := Entry{Conn: uuid.New(), Text: "select * from orders where id = 42", Rows: 1, Duration: time.Millisecond}
	for b.Loop() {
		if err := s.AppendHistory(e); err != nil {
			b.Fatal(err)
		}
	}
}
