package redis

import (
	"reflect"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestFields(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"GET foo", []string{"GET", "foo"}, false},
		{"  SET  a   b  ", []string{"SET", "a", "b"}, false},
		{`SET k "hello world"`, []string{"SET", "k", "hello world"}, false},
		{`SET k "a\nb\tc\\d\"e"`, []string{"SET", "k", "a\nb\tc\\d\"e"}, false},
		{`SET k "\x41\x7a\xff"`, []string{"SET", "k", "Az\xff"}, false},
		{`SET k "\r\a\b\q"`, []string{"SET", "k", "\r\a\bq"}, false},
		{`SET k 'it''s'`, nil, true},
		{`SET k 'a\'b'`, []string{"SET", "k", "a'b"}, false},
		{`SET k 'a\nb'`, []string{"SET", "k", `a\nb`}, false},
		{`SET k ""`, []string{"SET", "k", ""}, false},
		{`SET k ''`, []string{"SET", "k", ""}, false},
		{`SET a"b c" x`, []string{"SET", "ab c", "x"}, false},
		{`SET "k" "v"`, []string{"SET", "k", "v"}, false},
		{`SET "unterminated`, nil, true},
		{`SET 'unterminated`, nil, true},
		{`SET "a"b`, nil, true},
		{"HSET h f \"ünïcödé\"", []string{"HSET", "h", "f", "ünïcödé"}, false},
	}
	for _, tt := range tests {
		got, err := Fields(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("Fields(%q) err = %v, want err %v", tt.in, err, tt.err)
			continue
		}
		if !tt.err && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Fields(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQuoteRoundTrip(t *testing.T) {
	for _, s := range []string{"plain", "", "with space", `q"uote`, "it's", `back\slash`, "#hash", "//x",
		"line\nbreak", "tab\t", "bin\x00\x01\xff", "ünï", "a:b:c", "\x7f"} {
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
		{Text: "INCR c", Offset: 52},
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
