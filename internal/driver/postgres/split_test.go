package postgres

import (
	"slices"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

func texts(stmts []driver.Statement) []string {
	var out []string
	for _, s := range stmts {
		out = append(out, s.Text)
	}
	return out
}

func TestSplit(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{"empty", "", nil},
		{"blank", "  \n\t ", nil},
		{"single no semicolon", "select 1", []string{"select 1"}},
		{"single with semicolon", "select 1;", []string{"select 1"}},
		{"two", "select 1; select 2;", []string{"select 1", "select 2"}},
		{"trailing whitespace trimmed", "select 1  \n ;\n", []string{"select 1"}},
		{"empty statements", ";;select 1;;  ;", []string{"select 1"}},
		{"string with semicolon", "select 'a;b'; select 2", []string{"select 'a;b'", "select 2"}},
		{"doubled quote", "select 'it''s; fine'; x", []string{"select 'it''s; fine'", "x"}},
		{"E string backslash", `select E'a\';b'; x`, []string{`select E'a\';b'`, "x"}},
		{"e string lower", `select e'\\'; x`, []string{`select e'\\'`, "x"}},
		{"plain backslash not escape", `select 'a\'; x`, []string{`select 'a\'`, "x"}},
		{"identifier ending in e", `select 1 as e, 'x\'; y`, []string{`select 1 as e, 'x\'`, "y"}},
		{"quoted identifier", `select 1 as "a;""b"; x`, []string{`select 1 as "a;""b"`, "x"}},
		{"dollar quote", "do $$ begin perform 1; end $$; select 2", []string{"do $$ begin perform 1; end $$", "select 2"}},
		{"tagged dollar quote", "select $fn$ a; $$ b; $fn$; x", []string{"select $fn$ a; $$ b; $fn$", "x"}},
		{"param is not dollar quote", "select $1; select $2", []string{"select $1", "select $2"}},
		{"dollar inside identifier", "select a$b; x", []string{"select a$b", "x"}},
		{"line comment", "select 1; -- a; b\nselect 2", []string{"select 1", "-- a; b\nselect 2"}},
		{"comment only dropped", "select 1; -- done;", []string{"select 1"}},
		{"block comment only dropped", "/* a; b */ ; select 1", []string{"select 1"}},
		{"nested block comment", "select /* a /* b; */ c; */ 1; x", []string{"select /* a /* b; */ c; */ 1", "x"}},
		{"leading comment kept", "  /* hdr */ select 1", []string{"/* hdr */ select 1"}},
		{"unterminated string", "select 'abc; def", []string{"select 'abc; def"}},
		{"unterminated comment", "select 1; /* never; closed", []string{"select 1"}},
		{"parentheses", "create rule r as on insert to t do also (insert into a values (1); insert into b values (2)); x",
			[]string{"create rule r as on insert to t do also (insert into a values (1); insert into b values (2))", "x"}},
		{"begin atomic", "create function f() returns int language sql begin atomic select 1; select case when true then 2 end; end; select 3",
			[]string{"create function f() returns int language sql begin atomic select 1; select case when true then 2 end; end", "select 3"}},
		{"or replace procedure", "CREATE OR REPLACE PROCEDURE p() BEGIN ATOMIC INSERT INTO t VALUES (1); END; x",
			[]string{"CREATE OR REPLACE PROCEDURE p() BEGIN ATOMIC INSERT INTO t VALUES (1); END", "x"}},
		{"transaction begin splits", "begin; select 1; commit;", []string{"begin", "select 1", "commit"}},
		{"multibyte", "select 'ção'; select 'ü'", []string{"select 'ção'", "select 'ü'"}},
		{"crlf", "select 1;\r\nselect 2;\r\n", []string{"select 1", "select 2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Split(tt.script)
			if !slices.Equal(texts(got), tt.want) {
				t.Fatalf("Split(%q) = %q, want %q", tt.script, texts(got), tt.want)
			}
			for _, s := range got {
				if tt.script[s.Offset:s.Offset+len(s.Text)] != s.Text {
					t.Errorf("offset %d does not point at %q", s.Offset, s.Text)
				}
			}
		})
	}
}

func TestSplitOffsets(t *testing.T) {
	script := "select 1;\n\n  select 2 ;\n-- c\nselect 3"
	got := Split(script)
	want := []driver.Statement{
		{Text: "select 1", Offset: 0},
		{Text: "select 2", Offset: 13},
		{Text: "-- c\nselect 3", Offset: 24},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStatementAt(t *testing.T) {
	script := "select 1;\n\nselect 2;   \n\n  select 3"
	stmts := Split(script)
	// offsets: select 1 @0..8 (; at 8), select 2 @11..18 (; at 19), select 3 @27
	tests := []struct {
		offset int
		want   string
	}{
		{0, "select 1"},
		{4, "select 1"},
		{8, "select 1"},  // on the semicolon
		{9, "select 1"},  // right after it
		{10, "select 1"}, // blank line after
		{11, "select 2"},
		{20, "select 2"}, // after semicolon, trailing spaces
		{23, "select 2"}, // end of line
		{24, "select 2"}, // blank line
		{25, "select 3"}, // indentation on the line of select 3
		{27, "select 3"},
		{len(script), "select 3"},
		{len(script) + 10, "select 3"},
		{-5, "select 1"},
	}
	for _, tt := range tests {
		got, ok := StatementAt(stmts, script, tt.offset)
		if !ok || got.Text != tt.want {
			t.Errorf("StatementAt(%d) = %q, %v; want %q", tt.offset, got.Text, ok, tt.want)
		}
	}
	if _, ok := StatementAt(nil, "", 0); ok {
		t.Error("StatementAt on empty script should report false")
	}
	lead := "\n\n  select 1;"
	if got, ok := StatementAt(Split(lead), lead, 0); !ok || got.Text != "select 1" {
		t.Errorf("cursor before first statement: got %q, %v", got.Text, ok)
	}
}

func TestKeywords(t *testing.T) {
	got := keywords("/* c */ COPY t (a) FROM STDIN -- x\n WITH 'stdout'")
	want := []string{"copy", "t", "a", "from", "stdin", "with"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
