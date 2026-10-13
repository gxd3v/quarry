package export

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/postgres"
)

// Integration tests run against the server named by QUARRY_TEST_PG_DSN, e.g.
// postgres://postgres:quarry@localhost:55432/postgres?sslmode=disable

type pgLive struct {
	conn driver.Conn
	cfg  driver.Config
}

func (l *pgLive) Live(string) (driver.Conn, driver.Config, error) { return l.conn, l.cfg, nil }
func (l *pgLive) Config(string) (driver.Config, bool)             { return l.cfg, true }

func openPG(t *testing.T, readOnly bool) *pgLive {
	t.Helper()
	dsn := os.Getenv("QUARRY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QUARRY_TEST_PG_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	cfg := driver.Config{
		ID:       uuid.New(),
		Kind:     driver.Postgres,
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"),
		ReadOnly: readOnly,
		Options:  map[string]string{},
	}
	for k, v := range u.Query() {
		cfg.Options[k] = v[0]
	}
	conn, err := postgres.New().Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &pgLive{conn: conn, cfg: cfg}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	buf := make([]byte, 1<<20)
	n := 0
	for {
		k, err := r.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if err == io.EOF {
			return n
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestITMillionRowsCSV(t *testing.T) {
	live := openPG(t, true) // exports only read
	s := New(live)
	path := filepath.Join(t.TempDir(), "million.csv")
	req := sqlReq(path, "select g, md5(g::text) from generate_series(1, 1000000) g", &quarryv1.ExportOptions{Format: "csv", Header: true, Crlf: true})

	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	base, peak := ms.HeapInuse, ms.HeapInuse
	sample := func() {
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapInuse)
	}
	var msgs []*quarryv1.ExportResponse
	start := time.Now()
	err := s.export(t.Context(), req, func(m *quarryv1.ExportResponse) error {
		sample()
		msgs = append(msgs, m)
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if last := msgs[len(msgs)-1]; last.Rows != 1_000_000 || !last.Done {
		t.Fatalf("last = %v", last)
	}
	if n := countLines(t, path); n != 1_000_001 {
		t.Fatalf("%d lines", n)
	}
	fi, _ := os.Stat(path)
	t.Logf("1M rows: %v, %.1f MB file, %d messages, peak heap in use +%.1f MB",
		elapsed.Round(time.Millisecond), float64(fi.Size())/(1<<20), len(msgs), float64(peak-base)/(1<<20))
}

func TestITDataSourceSQL(t *testing.T) {
	live := openPG(t, false)
	ctx := t.Context()
	sess, err := live.conn.NewSession(ctx, driver.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	exec := func(script string) {
		t.Helper()
		for _, st := range postgres.Split(script) {
			r, err := sess.Execute(context.Background(), st)
			if err != nil {
				t.Fatalf("%s: %v", st.Text, err)
			}
			r.Close()
		}
	}
	exec(`drop schema if exists quarry_export_it cascade`)
	exec(`create schema quarry_export_it`)
	t.Cleanup(func() { exec(`drop schema if exists quarry_export_it cascade`) })
	exec(`create table quarry_export_it."Items" (id int primary key, name text, note text)`)
	exec(`insert into quarry_export_it."Items" values (1, 'a', null), (2, 'it''s', 'x'), (3, 'c', 'y')`)

	path := filepath.Join(t.TempDir(), "items.sql")
	req := &quarryv1.ExportRequest{
		ConnId: "pg",
		Path:   path,
		Source: &quarryv1.ExportRequest_Data{Data: &quarryv1.DataSource{
			Node:    &quarryv1.Node{Path: []string{live.cfg.Database, "quarry_export_it", "tables", "Items"}, Kind: "table", Name: "Items"},
			Filter:  "id >= 2 or note is null",
			OrderBy: []string{"id DESC"},
		}},
		Options: &quarryv1.ExportOptions{Format: "sql"},
	}
	if err := New(live).export(ctx, req, new(recorder).send); err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO quarry_export_it."Items" (id, name, note) VALUES (3, 'c', 'y');
INSERT INTO quarry_export_it."Items" (id, name, note) VALUES (2, 'it''s', 'x');
INSERT INTO quarry_export_it."Items" (id, name, note) VALUES (1, 'a', NULL);
`
	if got := read(t, path); got != want {
		t.Fatalf("content:\n%s\nwant:\n%s", got, want)
	}

	// The file round-trips: re-importing it reproduces the rows.
	exec(`create table quarry_export_it.copy (like quarry_export_it."Items")`)
	exec(strings.ReplaceAll(want, `quarry_export_it."Items"`, "quarry_export_it.copy"))
	r, err := sess.Execute(ctx, driver.Statement{Text: `select count(*) from (select * from quarry_export_it."Items" except select * from quarry_export_it.copy) d`})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rows, err := r.Next(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0][0] != int64(0) {
		t.Fatalf("difference = %v, %v", rows, err)
	}
}
