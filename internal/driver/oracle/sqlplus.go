package oracle

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gxd3v/quarry/internal/driver"
)

// unsupportedCommands are SQL*Plus commands that would need a real
// SQL*Plus; the others are accepted and ignored.
var unsupportedCommands = []string{"connect", "conn", "disconnect", "host", "start", "run", "get", "edit", "ed",
	"password", "startup", "shutdown", "recover", "archive", "accept", "pause", "input", "append", "change", "save", "store", "list"}

// sqlplus runs the SQL*Plus commands an editor script may contain:
//
//   - EXEC[UTE] call → BEGIN call; END;
//   - SET SERVEROUTPUT ON|OFF enables or disables DBMS_OUTPUT collection
//   - PROMPT text → an OUTPUT message
//   - DESC[RIBE] [schema.]name → a Name / Null? / Type result
//   - @script, CONNECT, HOST, ... → an error
//   - anything else (SET LINESIZE, COLUMN, SPOOL, ...) is ignored with an
//     INFO message.
func (s *session) sqlplus(ctx context.Context, text string, toks []token) (driver.Result, error) {
	cmd := strings.ToUpper(toks[0].low)
	rest := strings.TrimRight(strings.TrimSpace(text[toks[0].end:]), "; \t\r\n")
	switch {
	case toks[0].kind != tokWord || slices.Contains(unsupportedCommands, toks[0].low):
		return nil, fmt.Errorf("SQL*Plus command %s is not supported here; use the Shell", strings.Fields(text[toks[0].start:])[0])
	case cmd == "EXEC" || cmd == "EXECUTE":
		return s.exec(ctx, text, rest)
	case cmd == "PROMPT" || cmd == "PRO":
		s.addMsg("OUTPUT", rest)
		return &memResult{tag: "PROMPT"}, nil
	case cmd == "DESC" || cmd == "DESCRIBE":
		return s.describe(ctx, rest)
	case cmd == "SET":
		if w := keywords(rest); len(w) >= 2 && (w[0] == "serveroutput" || w[0] == "serverout") && (w[1] == "on" || w[1] == "off") {
			block := "BEGIN DBMS_OUTPUT.ENABLE(NULL); END;"
			if w[1] == "off" {
				block = "BEGIN DBMS_OUTPUT.DISABLE; END;"
			}
			if err := s.call(ctx, block, func() error { _, err := s.link.exec(ctx, block); return err }); err != nil {
				return nil, s.failure(err, "")
			}
			return &memResult{tag: "SET"}, nil
		}
	}
	s.addMsg("INFO", "SQL*Plus command ignored: "+firstLine(text[toks[0].start:]))
	return &memResult{tag: cmd}, nil
}

// exec runs EXEC call as an anonymous block; error positions are mapped back
// to the call.
func (s *session) exec(ctx context.Context, text, call string) (driver.Result, error) {
	const prefix = "BEGIN "
	block := prefix + call + "; END;"
	if err := s.checkWrite(block, codeTokens(block)); err != nil {
		return nil, err
	}
	r, err := s.run(ctx, block, codeTokens(block), true)
	if qe, ok := errors.AsType[*driver.QueryError](err); ok && qe.Position >= 0 {
		q := *qe
		q.Position = -1
		if p := qe.Position - len(prefix); p >= 0 && p < len(call) {
			q.Position = strings.Index(text, call) + p
		}
		return nil, &q
	}
	return r, err
}

const describeQuery = `
SELECT column_name, nullable, data_type, data_length, data_precision, data_scale, char_length, char_used, data_type_owner
FROM all_tab_columns
WHERE owner = NVL(:1, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND table_name = :2
ORDER BY column_id`

// describe lists a table's or view's columns like SQL*Plus DESCRIBE.
func (s *session) describe(ctx context.Context, name string) (driver.Result, error) {
	owner, obj, ok := splitName(name)
	if !ok {
		return nil, fmt.Errorf("usage: DESCRIBE [schema.]object")
	}
	m := &memResult{
		cols: []driver.Column{{Name: "Name", Type: "VARCHAR2"}, {Name: "Null?", Type: "VARCHAR2"}, {Name: "Type", Type: "VARCHAR2"}},
		tag:  "DESCRIBE",
	}
	err := s.call(ctx, describeQuery, func() error {
		return s.link.query(ctx, describeQuery, []any{owner, obj}, func(row []sqldriver.Value) error {
			null := any(nil)
			if str(row[1]) == "N" {
				null = "NOT NULL"
			}
			m.rows = append(m.rows, []any{str(row[0]), null, typeOf(row[2:])})
			return nil
		})
	})
	if err != nil {
		return nil, s.failure(err, "")
	}
	if len(m.rows) == 0 {
		return nil, &driver.QueryError{Code: "ORA-04043", Msg: fmt.Sprintf("object %s does not exist", name), Position: -1}
	}
	return m, nil
}

// splitName parses [schema.]name as SQL would: quoted parts are exact,
// others upper-cased.
func splitName(s string) (owner, name string, ok bool) {
	var parts []string
	toks := lex(s)
	for k, t := range toks {
		if t.kind == tokOther && s[t.start:t.end] == "." {
			continue
		}
		p, ok := identAt(s, toks, k)
		if !ok {
			return "", "", false
		}
		parts = append(parts, p)
	}
	switch len(parts) {
	case 1:
		return "", parts[0], true
	case 2:
		return parts[0], parts[1], true
	}
	return "", "", false
}

// typeOf formats a column type from all_tab_columns fields: data_type,
// data_length, data_precision, data_scale, char_length, char_used,
// data_type_owner.
func typeOf(f []sqldriver.Value) string {
	typ := str(f[0])
	length, charLen := integer(f[1]), integer(f[4])
	switch typ {
	case "VARCHAR2", "VARCHAR", "CHAR":
		if str(f[5]) == "C" {
			return fmt.Sprintf("%s(%d CHAR)", typ, charLen)
		}
		return fmt.Sprintf("%s(%d)", typ, length)
	case "NVARCHAR2", "NCHAR":
		return fmt.Sprintf("%s(%d)", typ, charLen)
	case "NUMBER":
		switch {
		case f[2] == nil && f[3] == nil:
			return "NUMBER"
		case f[2] == nil:
			return fmt.Sprintf("NUMBER(*,%d)", integer(f[3]))
		case integer(f[3]) == 0:
			return fmt.Sprintf("NUMBER(%d)", integer(f[2]))
		}
		return fmt.Sprintf("NUMBER(%d,%d)", integer(f[2]), integer(f[3]))
	case "FLOAT":
		if f[2] != nil {
			return fmt.Sprintf("FLOAT(%d)", integer(f[2]))
		}
	case "RAW", "UROWID":
		return fmt.Sprintf("%s(%d)", typ, length)
	}
	if o := str(f[6]); o != "" && o != "SYS" && o != "PUBLIC" {
		return quoteIdent(o) + "." + quoteIdent(typ)
	}
	return typ
}

// unitObject is the object a CREATE of a PL/SQL unit compiles.
type unitObject struct {
	typ, owner, name string
	base             int // offset of the source's first line, -1 if unknown
}

// compiledObject finds the object created by a PL/SQL unit statement.
func compiledObject(text string, toks []token) (unitObject, bool) {
	w := func(k int) string {
		if k < len(toks) && toks[k].kind == tokWord {
			return toks[k].low
		}
		return ""
	}
	if w(0) != "create" {
		return unitObject{}, false
	}
	k := 1
	force := false
	for objectModifiers[w(k)] {
		force = force || w(k) == "force"
		k++
	}
	u := unitObject{typ: strings.ToUpper(w(k)), base: -1}
	if k < len(toks) {
		u.base = toks[k].start
	}
	switch w(k) {
	case "procedure", "function", "library":
	case "package", "type":
		if w(k+1) == "body" {
			u.typ += " BODY"
			k++
		}
	case "trigger":
		u.base = -1
		for _, t := range toks[k:] {
			if t.kind == tokWord && (t.low == "declare" || t.low == "begin" || t.low == "compound") {
				u.base = t.start
				break
			}
		}
	case "view":
		if !force {
			return unitObject{}, false
		}
		u.base = -1
	default:
		return unitObject{}, false
	}
	k++
	if w(k) == "if" && w(k+1) == "not" && w(k+2) == "exists" {
		k += 3
	}
	first, ok := identAt(text, toks, k)
	if !ok {
		return unitObject{}, false
	}
	u.name = first
	if k+2 < len(toks) && text[toks[k+1].start:toks[k+1].end] == "." {
		if second, ok := identAt(text, toks, k+2); ok {
			u.owner, u.name = first, second
		}
	}
	return u, true
}

// identAt returns the identifier at toks[k] as the server stores it: quoted
// names exactly, others upper-cased.
func identAt(text string, toks []token, k int) (string, bool) {
	if k >= len(toks) {
		return "", false
	}
	t := toks[k]
	switch {
	case t.kind == tokWord:
		return strings.ToUpper(text[t.start:t.end]), true
	case t.kind == tokOther && text[t.start] == '"' && t.end-t.start >= 2:
		return text[t.start+1 : t.end-1], true
	}
	return "", false
}

// position maps an all_errors line and column (1-based, characters) to a
// byte offset in text, counting lines from u.base.
func (u unitObject) position(text string, line, col int) int {
	if u.base < 0 || line < 1 || col < 1 {
		return -1
	}
	off := u.base
	for range line - 1 {
		j := strings.IndexByte(text[off:], '\n')
		if j < 0 {
			return -1
		}
		off += j + 1
	}
	for range col - 1 {
		if off >= len(text) || text[off] == '\n' {
			return -1
		}
		_, n := utf8.DecodeRuneInString(text[off:])
		off += n
	}
	if off >= len(text) {
		return -1
	}
	return off
}

const compileErrorsQuery = `
SELECT line, position, text, attribute
FROM all_errors
WHERE owner = NVL(:1, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND name = :2 AND type = :3
ORDER BY sequence`

// compileErrors reports the errors of a PL/SQL unit just created: the
// server accepts the statement and stores the unit as invalid (SQL*Plus
// prints "Warning: ... created with compilation errors"). Errors become a
// *driver.QueryError pointing at the first one, warnings become messages.
func (s *session) compileErrors(ctx context.Context, text string, toks []token) error {
	u, ok := compiledObject(text, toks)
	if !ok {
		return nil
	}
	type entry struct {
		line, col  int
		text, attr string
	}
	var errs []entry
	ctx = context.WithoutCancel(ctx)
	err := s.link.query(ctx, compileErrorsQuery, []any{u.owner, u.name, u.typ}, func(row []sqldriver.Value) error {
		e := entry{int(integer(row[0])), int(integer(row[1])), strings.TrimSpace(str(row[2])), str(row[3])}
		if e.attr == "WARNING" {
			s.addMsg("WARNING", fmt.Sprintf("%s %s line %d:%d %s", u.typ, u.name, e.line, e.col, e.text))
			return nil
		}
		errs = append(errs, e)
		return nil
	})
	if err != nil || len(errs) == 0 {
		s.link.check(err)
		return nil
	}
	lines := make([]string, len(errs))
	for i, e := range errs {
		lines[i] = fmt.Sprintf("line %d:%d %s", e.line, e.col, e.text)
	}
	qe := &driver.QueryError{
		Msg:      errs[0].text,
		Detail:   fmt.Sprintf("%s %s was created with compilation errors:\n%s", strings.ToLower(u.typ), u.name, strings.Join(lines, "\n")),
		Position: u.position(text, errs[0].line, errs[0].col),
	}
	if m := codeRe.FindStringSubmatch(qe.Msg); m != nil {
		qe.Code, qe.Msg = m[1], qe.Msg[len(m[0]):]
	}
	return qe
}
