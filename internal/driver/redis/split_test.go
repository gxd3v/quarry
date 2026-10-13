package redis

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestFields(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		pos  int // error position, -1 for no error
	}{
		{"", nil, -1},
		{"   ", nil, -1},
		{"GET foo", []string{"GET", "foo"}, -1},
		{"  SET  a   b  ", []string{"SET", "a", "b"}, -1},
		{`SET k "hello world"`, []string{"SET", "k", "hello world"}, -1},
		{`SET k "a\nb\tc\\d\"e"`, []string{"SET", "k", "a\nb\tc\\d\"e"}, -1},
		{`SET k "\x41\x7a\xff"`, []string{"SET", "k", "Az\xff"}, -1},
		{`SET k "\r\a\b\q"`, []string{"SET", "k", "\r\a\bq"}, -1},
		{`SET k 'it''s'`, nil, 9},
		{`SET k 'a\'b'`, []string{"SET", "k", "a'b"}, -1},
		{`SET k 'a\nb'`, []string{"SET", "k", `a\nb`}, -1},
		{`SET k ""`, []string{"SET", "k", ""}, -1},
		{`SET k ''`, []string{"SET", "k", ""}, -1},
		{`SET a"b c" x`, []string{"SET", "ab c", "x"}, -1},
		{`SET "k" "v"`, []string{"SET", "k", "v"}, -1},
		{`SET "unterminated`, nil, 4},
		{`SET k 'unterminated`, nil, 6},
		{`SET "a"b`, nil, 6},
		{"HSET h f \"ünïcödé\"", []string{"HSET", "h", "f", "ünïcödé"}, -1},
	}
	for _, tt := range tests {
		got, err := Fields(tt.in)
		if tt.pos >= 0 {
			se, ok := errors.AsType[*syntaxError](err)
			if !ok || se.pos != tt.pos {
				t.Errorf("Fields(%q) err = %v, want a syntax error at %d", tt.in, err, tt.pos)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Fields(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestQuoteRoundTrip(t *testing.T) {
	for _, s := range []string{"plain", "", "with space", `q"uote`, "it's", `back\slash`, "#hash", "//x",
		"line\nbreak", "tab\t", "bin\x00\x01\xff", "ünï", "a:b:c", "\x7f", "\x00k:x"} {
		q := Quote(s)
		got, err := Fields("SET " + q)
		if err != nil || len(got) != 2 || got[1] != s {
			t.Errorf("Quote(%q) = %s, read back %q, %v", s, q, got, err)
		}
	}
	if Quote("user:1") != "user:1" {
		t.Errorf("plain argument quoted: %s", Quote("user:1"))
	}
	if got := command("SET", "a b", "c"); got != `SET "a b" c` {
		t.Errorf("command = %s", got)
	}
}

func TestSplit(t *testing.T) {
	script := "GET a\n\n# comment\n  SET b \"x y\"  \r\n\t// also a comment\nINCR c"
	got := Split(script)
	want := []driver.Statement{
		{Text: "GET a", Offset: 0},
		{Text: `SET b "x y"`, Offset: 19},
		{Text: "INCR c", Offset: 53},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split = %+v, want %+v", got, want)
	}
	for _, s := range got {
		if script[s.Offset:s.Offset+len(s.Text)] != s.Text {
			t.Errorf("offset %d does not point at %q", s.Offset, s.Text)
		}
	}
	if Split("  \n# only comments\n") != nil {
		t.Error("expected no statements")
	}
}

func TestStatementAt(t *testing.T) {
	script := "GET a\n\nSET b 1\n# note\n\nINCR c\n"
	stmts := Split(script)
	tests := []struct {
		offset int
		want   string
	}{
		{0, "GET a"},
		{5, "GET a"},   // end of the line
		{6, "GET a"},   // blank line below it
		{7, "SET b 1"}, // start of the next command
		{10, "SET b 1"},
		{16, "SET b 1"}, // inside the comment line
		{23, "INCR c"},
		{len(script), "INCR c"},
		{-5, "GET a"},
	}
	for _, tt := range tests {
		st, ok := StatementAt(stmts, script, tt.offset)
		if !ok || st.Text != tt.want {
			t.Errorf("StatementAt(%d) = %q, %v; want %q", tt.offset, st.Text, ok, tt.want)
		}
	}
	if _, ok := StatementAt(nil, "", 0); ok {
		t.Error("no statements should report false")
	}
}
