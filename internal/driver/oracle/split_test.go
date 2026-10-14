package oracle

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
		{"single no semicolon", "select 1 from dual", []string{"select 1 from dual"}},
		{"single with semicolon", "select 1 from dual;", []string{"select 1 from dual"}},
		{"two", "select 1 from dual; select 2 from dual;", []string{"select 1 from dual", "select 2 from dual"}},
		{"trailing whitespace trimmed", "select 1 from dual  \n ;\n", []string{"select 1 from dual"}},
		{"empty statements", ";;select 1 from dual;;  ;", []string{"select 1 from dual"}},
		{"string with semicolon", "select 'a;b' from dual; x", []string{"select 'a;b' from dual", "x"}},
		{"doubled quote", "select 'it''s; fine' from dual; x", []string{"select 'it''s; fine' from dual", "x"}},
		{"backslash is not an escape", `select 'a\' from dual; x`, []string{`select 'a\' from dual`, "x"}},
		{"q quote brackets", "select q'[a;'b]' from dual; x", []string{"select q'[a;'b]' from dual", "x"}},
		{"q quote braces", "select Q'{a;}' from dual; x", []string{"select Q'{a;}' from dual", "x"}},
		{"q quote angle", "select q'<a;>' from dual; x", []string{"select q'<a;>' from dual", "x"}},
		{"q quote parens", "select q'(a;))' from dual; x", []string{"select q'(a;))' from dual", "x"}},
		{"q quote same char", "select q'!a;'b!' from dual; x", []string{"select q'!a;'b!' from dual", "x"}},
		{"nq quote", "select nq'[a;]' from dual; x", []string{"select nq'[a;]' from dual", "x"}},
		{"national string", "select N'a;b' from dual; x", []string{"select N'a;b' from dual", "x"}},
		{"identifier starting with q", "select q1, n from t; x", []string{"select q1, n from t", "x"}},
		{"quoted identifier", `select 1 "a;b" from dual; x`, []string{`select 1 "a;b" from dual`, "x"}},
		{"identifier with $ and #", "select a$b#c from t; x", []string{"select a$b#c from t", "x"}},
		{"line comment", "select 1 from dual; -- a; b\nselect 2 from dual", []string{"select 1 from dual", "-- a; b\nselect 2 from dual"}},
		{"comment only dropped", "select 1 from dual; -- done;", []string{"select 1 from dual"}},
		{"block comment only dropped", "/* a; b */ ; select 1 from dual", []string{"select 1 from dual"}},
		{"block comments do not nest", "select /* a /* b */ 1 from dual; x", []string{"select /* a /* b */ 1 from dual", "x"}},
		{"leading comment kept", "  /* hdr */ select 1 from dual", []string{"/* hdr */ select 1 from dual"}},
		{"unterminated string", "select 'abc; def", []string{"select 'abc; def"}},
		{"unterminated comment", "select 1 from dual; /* never; closed", []string{"select 1 from dual"}},
		{"parentheses", "insert into t (a, b) values (1, ';'); x", []string{"insert into t (a, b) values (1, ';')", "x"}},
		{"view with case is SQL", "create view v as select case when 1 = 1 then 'a' end x from dual; select 1 from dual",
			[]string{"create view v as select case when 1 = 1 then 'a' end x from dual", "select 1 from dual"}},
		{"multibyte", "select 'ção' from dual; select '€' from dual", []string{"select 'ção' from dual", "select '€' from dual"}},
		{"crlf", "select 1 from dual;\r\nselect 2 from dual;\r\n", []string{"select 1 from dual", "select 2 from dual"}},

		{"slash ends SQL", "select 1 from dual\n/\nselect 2 from dual\n/\n", []string{"select 1 from dual", "select 2 from dual"}},
		{"slash after semicolon dropped", "select 1 from dual;\n/\nselect 2 from dual;", []string{"select 1 from dual", "select 2 from dual"}},
		{"division is not a terminator", "select 4\n/ 2 from dual;", []string{"select 4\n/ 2 from dual"}},
		{"indented slash", "select 1 from dual\n  /  \nx", []string{"select 1 from dual", "x"}},
		{"slash in comment", "select 1 /*\n/\n*/ from dual; x", []string{"select 1 /*\n/\n*/ from dual", "x"}},
		{"slash in string", "select '\n/\n' from dual; x", []string{"select '\n/\n' from dual", "x"}},

		{"block with slash", "begin\n  null;\nend;\n/\nselect 1 from dual;",
			[]string{"begin\n  null;\nend;", "select 1 from dual"}},
		{"block without slash", "declare\n  x number;\nbegin\n  x := 1;\nend;\nselect 1 from dual",
			[]string{"declare\n  x number;\nbegin\n  x := 1;\nend;", "select 1 from dual"}},
		{"control structures", "begin\n  if 1 = 1 then\n    for i in 1 .. 2 loop null; end loop;\n  end if;\n" +
			"  case when 1 = 1 then null; end case;\n  x := case when y then 1 else 2 end;\n" +
			"  begin null; exception when others then null; end;\nend;\nselect 2 from dual",
			[]string{"begin\n  if 1 = 1 then\n    for i in 1 .. 2 loop null; end loop;\n  end if;\n" +
				"  case when 1 = 1 then null; end case;\n  x := case when y then 1 else 2 end;\n" +
				"  begin null; exception when others then null; end;\nend;", "select 2 from dual"}},
		{"labels and named end", "<<outer>> begin null; end outer; select 1 from dual",
			[]string{"<<outer>> begin null; end outer;", "select 1 from dual"}},
		{"block declaring a procedure", "declare\n  procedure p is begin null; end;\nbegin\n  p;\nend;\nx",
			[]string{"declare\n  procedure p is begin null; end;\nbegin\n  p;\nend;", "x"}},
		{"block with select into", "begin select count(*) into n from t where a in (select 1 from dual); end; x",
			[]string{"begin select count(*) into n from t where a in (select 1 from dual); end;", "x"}},
		{"procedure without slash", "create or replace procedure p(a number) is\n  cursor c is select 1 from dual;\n" +
			"  procedure inner is begin null; end;\nbegin\n  inner;\nend p;\nselect 1 from dual",
			[]string{"create or replace procedure p(a number) is\n  cursor c is select 1 from dual;\n" +
				"  procedure inner is begin null; end;\nbegin\n  inner;\nend p;", "select 1 from dual"}},
		{"editionable function", "CREATE OR REPLACE EDITIONABLE FUNCTION f(x IN NUMBER) RETURN NUMBER AS\nBEGIN\n  RETURN x * 2;\nEND;\n/\nselect f(1) from dual",
			[]string{"CREATE OR REPLACE EDITIONABLE FUNCTION f(x IN NUMBER) RETURN NUMBER AS\nBEGIN\n  RETURN x * 2;\nEND;", "select f(1) from dual"}},
		{"package spec", "create or replace package pk as\n  type rec is record (a number);\n  procedure a;\n  function f(x number) return number;\nend pk;\nselect 1 from dual",
			[]string{"create or replace package pk as\n  type rec is record (a number);\n  procedure a;\n  function f(x number) return number;\nend pk;", "select 1 from dual"}},
		{"package body", "create package body pk as\n  procedure a is begin null; end;\n  function f(x number) return number is\n  begin\n    return x;\n  end;\nbegin\n  null;\nend pk;\nselect 1 from dual",
			[]string{"create package body pk as\n  procedure a is begin null; end;\n  function f(x number) return number is\n  begin\n    return x;\n  end;\nbegin\n  null;\nend pk;", "select 1 from dual"}},
		{"type spec", "create type t as object (a number, member function f return number);\n/\nselect 1 from dual",
			[]string{"create type t as object (a number, member function f return number);", "select 1 from dual"}},
		{"collection type", "create or replace type nums is table of number;\nselect 1 from dual",
			[]string{"create or replace type nums is table of number;", "select 1 from dual"}},
		{"type body", "create type body t as\n  member function f return number is begin return a; end;\nend;\nselect 1 from dual",
			[]string{"create type body t as\n  member function f return number is begin return a; end;\nend;", "select 1 from dual"}},
		{"trigger", "create or replace trigger trg before insert on t for each row\nbegin\n  :new.id := 1;\nend;\n/\ninsert into t values (1);",
			[]string{"create or replace trigger trg before insert on t for each row\nbegin\n  :new.id := 1;\nend;", "insert into t values (1)"}},
		{"trigger without slash", "create trigger trg after delete on t declare n number; begin null; end;\nselect 1 from dual",
			[]string{"create trigger trg after delete on t declare n number; begin null; end;", "select 1 from dual"}},
		{"compound trigger needs the slash", "create trigger trg for insert on t compound trigger\n  before statement is begin null; end before statement;\nend trg;\n/\nselect 1 from dual",
			[]string{"create trigger trg for insert on t compound trigger\n  before statement is begin null; end before statement;\nend trg;", "select 1 from dual"}},
		{"call specification", "create procedure j as language java name 'X.f()';\nselect 1 from dual",
			[]string{"create procedure j as language java name 'X.f()';", "select 1 from dual"}},
		{"with function", "with function f return number is begin return 1; end;\nselect f from dual\n/\nselect 2 from dual",
			[]string{"with function f return number is begin return 1; end;\nselect f from dual", "select 2 from dual"}},
		{"java source", "create or replace and compile java source named \"X\" as public class X { int f() { return 1; } };\n/\nselect 1 from dual",
			[]string{"create or replace and compile java source named \"X\" as public class X { int f() { return 1; } };", "select 1 from dual"}},
		{"unterminated block", "begin\n  null;", []string{"begin\n  null;"}},
		{"block crlf", "begin\r\n  null;\r\nend;\r\n/\r\nselect 1 from dual;\r\n", []string{"begin\r\n  null;\r\nend;", "select 1 from dual"}},

		{"sqlplus commands", "set serveroutput on\nbegin dbms_output.put_line('x'); end;\n/\nprompt done;\nexec p(1);\ndesc emp\nselect 1 from dual;",
			[]string{"set serveroutput on", "begin dbms_output.put_line('x'); end;", "prompt done", "exec p(1)", "desc emp", "select 1 from dual"}},
		{"set transaction is SQL", "set transaction read only;\nselect 1 from dual", []string{"set transaction read only", "select 1 from dual"}},
		{"set role is SQL", "set role all;\nx", []string{"set role all", "x"}},
		{"rem is a comment", "rem hello; world\nREMARK x\nselect 1 from dual;", []string{"select 1 from dual"}},
		{"script call", "@foo.sql\n@@bar\nselect 1 from dual;", []string{"@foo.sql", "@@bar", "select 1 from dual"}},
		{"command word mid-statement", "select prompt from t\nwhere desc = 1;", []string{"select prompt from t\nwhere desc = 1"}},
		{"command after comment", "-- go\nexec p", []string{"-- go\nexec p"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Split(tt.script)
			if !slices.Equal(texts(got), tt.want) {
				t.Fatalf("Split(%q)\n got %q\nwant %q", tt.script, texts(got), tt.want)
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
	script := "select 1 from dual;\n\n  begin null; end;\n/\n-- c\nselect 3 from dual"
	got := Split(script)
	want := []driver.Statement{
		{Text: "select 1 from dual", Offset: 0},
		{Text: "begin null; end;", Offset: 23},
		{Text: "-- c\nselect 3 from dual", Offset: 42},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStatementAt(t *testing.T) {
	script := "select 1;\n\nselect 2;   \n\n  select 3"
	stmts := Split(script)
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

	plsql := "begin\n  null;\nend;\n/\n\nselect 1 from dual"
	stmts = Split(plsql)
	for _, tt := range []struct {
		offset int
		want   string
	}{
		{8, "begin\n  null;\nend;"},
		{19, "begin\n  null;\nend;"}, // on the slash line
		{22, "select 1 from dual"},
	} {
		if got, _ := StatementAt(stmts, plsql, tt.offset); got.Text != tt.want {
			t.Errorf("PL/SQL StatementAt(%d) = %q, want %q", tt.offset, got.Text, tt.want)
		}
	}
}

func TestExecText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"select 1 from dual", "select 1 from dual"},
		{"select 1 from dual;  ", "select 1 from dual"},
		{"select 1 from dual;; -- done", "select 1 from dual"},
		{"select 1 from dual\n/\n", "select 1 from dual"},
		{"begin null; end;", "begin null; end;"},
		{"begin null; end;\n/", "begin null; end;"},
		{"create procedure p is begin null; end;\n/\n", "create procedure p is begin null; end;"},
	}
	for _, tt := range tests {
		toks := codeTokens(tt.in)
		unit, _ := unitKind(toks)
		if got := execText(tt.in, toks, unit); got != tt.want {
			t.Errorf("execText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTagFor(t *testing.T) {
	tests := []struct {
		text     string
		affected int64
		want     string
	}{
		{"insert into t values (1)", 1, "INSERT 1"},
		{"UPDATE t SET a = 1", 3, "UPDATE 3"},
		{"create table t (a number)", -1, "CREATE TABLE"},
		{"create or replace editionable package body p as begin null; end;", -1, "CREATE PACKAGE BODY"},
		{"create materialized view mv as select 1 x from dual", -1, "CREATE MATERIALIZED VIEW"},
		{"create global temporary table g (a number)", -1, "CREATE TABLE"},
		{"drop table t purge", -1, "DROP TABLE"},
		{"alter session set nls_date_format = 'YYYY'", -1, "ALTER SESSION"},
		{"begin null; end;", -1, "PL/SQL BLOCK"},
		{"commit", -1, "COMMIT"},
		{"grant select on t to u", -1, "GRANT"},
	}
	for _, tt := range tests {
		if got := tagFor(codeWords(codeTokens(tt.text), 12), tt.affected); got != tt.want {
			t.Errorf("tagFor(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestSQLID(t *testing.T) {
	// Checked against v$sql on Oracle 23ai.
	if got := sqlID("select 'quarry probe' from dual"); got != "5ayhca09mmb8n" {
		t.Errorf("sqlID = %q", got)
	}
}
