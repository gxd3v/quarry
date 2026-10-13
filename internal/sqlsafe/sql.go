package sqlsafe

import (
	"fmt"
	"strings"
)

// maxDepth bounds recursion into CTEs, subqueries and DO bodies.
const maxDepth = 32

func analyzeSQL(s string) verdict {
	return script(lexSQL(s), 0)
}

// script analyzes a sequence of statements separated by semicolons. An
// anonymous block or a routine with an unquoted body (PL/SQL, BEGIN ATOMIC)
// swallows the rest of the input, since its semicolons are not separators.
func script(toks []token, depth int) verdict {
	v := verdict{known: true}
	for len(toks) > 0 {
		n := segmentEnd(toks)
		if seg := toks[:n]; len(seg) > 0 {
			if isBlock(seg) {
				return v.worse(block(toks, depth))
			}
			v = v.worse(stmt(seg, depth))
			if opensBody(seg) {
				return v
			}
		}
		if n >= len(toks) {
			break
		}
		toks = toks[n+1:]
	}
	return v
}

// isBlock reports whether seg starts a PL/SQL or PL/pgSQL anonymous block
// rather than a transaction (BEGIN [WORK]) or a cursor (DECLARE c CURSOR).
func isBlock(seg []token) bool {
	i := firstWord(seg)
	if i < 0 || i+1 >= len(seg) {
		return false
	}
	switch seg[i].low {
	case "begin":
		return !seg[i+1].is("transaction", "work", "isolation", "read", "deferrable", "not")
	case "declare":
		if seg[i+1].is("cursor") {
			return true
		}
		for _, t := range seg[i+2:] {
			switch {
			case t.is("cursor"):
				return false
			case !t.is("binary", "asensitive", "insensitive", "scroll", "no"):
				return true
			}
		}
		return true
	}
	return false
}

var routineKinds = []string{"function", "procedure", "package", "trigger", "type"}

var createModifiers = []string{"or", "replace", "temporary", "temp", "unlogged", "global",
	"local", "unique", "materialized", "recursive", "editionable", "noneditionable",
	"editioning", "force", "noforce", "public", "private", "trusted", "procedural",
	"constraint", "default", "shared", "bitmap", "multivalue"}

// opensBody reports whether seg is a CREATE of a routine whose body is not a
// single string literal, so its semicolons belong to the body.
func opensBody(seg []token) bool {
	i := firstWord(seg)
	if i < 0 || !seg[i].is("create") {
		return false
	}
	j := i + 1
	for j < len(seg) && seg[j].is(createModifiers...) {
		j++
	}
	if j >= len(seg) || !seg[j].is(routineKinds...) {
		return false
	}
	for _, k := range topLevel(seg[j:]) {
		t := seg[j+k]
		switch {
		case t.is("begin", "declare"):
			return true
		case t.is("is", "as") && j+k+1 < len(seg):
			next := seg[j+k+1]
			switch {
			case next.kind == tString || next.is("enum", "range"):
				return false
			case next.kind != tOpen:
				return true
			}
		}
	}
	return false
}

// blockDML are the statements looked for inside procedural code.
var blockDML = []string{"insert", "update", "delete", "merge", "drop", "truncate", "alter", "create", "grant", "revoke"}

// block analyzes an anonymous block: it is a write in itself, and any DML it
// contains directly (not in EXECUTE strings) can raise the risk.
func block(toks []token, depth int) verdict {
	verb := "BLOCK"
	if i := firstWord(toks); i >= 0 {
		verb = strings.ToUpper(toks[i].low)
	}
	v := ok(Write, verb, "Anonymous block runs procedural code that may modify data")
	if depth > maxDepth {
		return v
	}
	for len(toks) > 0 {
		n := segmentEnd(toks)
		seg := toks[:n]
		for _, k := range topLevel(seg) {
			if !seg[k].is(blockDML...) {
				continue
			}
			if k > 0 && !seg[k-1].is("begin", "then", "else", "loop", "is", "as", "query") && seg[k-1].raw != ">" {
				continue
			}
			sv := stmt(seg[k:], depth+1)
			sv.known = true
			v = v.worse(sv)
			break
		}
		if n >= len(toks) {
			break
		}
		toks = toks[n+1:]
	}
	return v
}

// stmt analyzes a single statement (no top-level semicolons).
func stmt(seg []token, depth int) verdict {
	if depth > maxDepth {
		return unknown("")
	}
	i := firstWord(seg)
	if i < 0 {
		return verdict{known: true}
	}
	toks := seg[i:]
	w := toks[0].low
	verb := strings.ToUpper(w)
	switch w {
	case "with":
		return with(toks, depth)
	case "select":
		return selectStmt(toks)
	case "values", "table", "show", "describe", "desc", "set", "reset", "begin", "start",
		"commit", "rollback", "abort", "end", "savepoint", "release", "fetch", "move",
		"close", "listen", "unlisten", "discard", "deallocate", "prepare":
		return ok(None, verb, "")
	case "declare": // PostgreSQL cursor; blocks were handled by script
		for _, k := range topLevel(toks) {
			if toks[k].is("for") {
				return stmt(toks[k+1:], depth+1)
			}
		}
		return unknown(verb)
	case "explain":
		return explain(toks, depth)
	case "insert", "merge", "upsert", "replace":
		n, _ := name(toks, skip(toks, 1, "into"))
		return ok(Write, verb, fmt.Sprintf("%s writes to %s", verb, orTarget(n)))
	case "update", "delete":
		return updateDelete(toks, verb)
	case "copy":
		return copyStmt(toks, depth)
	case "do":
		v := ok(Write, verb, "DO runs procedural code that may modify data")
		for _, t := range toks[1:] {
			if t.kind == tString {
				return v.worse(block(lexSQL(unquote(t.raw)), depth+1))
			}
		}
		return v
	case "call", "execute", "exec":
		n, _ := name(toks, skip(toks, 1, "immediate"))
		return ok(Write, verb, fmt.Sprintf("%s runs %s, which may modify data", verb, orName(n, "code")))
	case "lock":
		return ok(Write, verb, "LOCK blocks other sessions until the transaction ends")
	case "vacuum", "analyze", "analyse", "cluster", "reindex", "refresh", "checkpoint", "load":
		return ok(Write, verb, verb+" performs maintenance writes")
	case "create":
		return create(toks)
	case "alter":
		return alter(toks)
	case "grant", "revoke":
		return ok(DDL, verb, verb+" changes privileges")
	case "comment", "rename", "audit", "noaudit", "security", "import", "flashback",
		"associate", "disassociate":
		return ok(DDL, verb, verb+" changes the schema")
	case "drop":
		return drop(toks)
	case "truncate":
		j := skip(toks, 1, "table")
		j = skip(toks, j, "only")
		n, _ := name(toks, j)
		return ok(Destructive, verb, "TRUNCATE removes every row of "+orTarget(n))
	case "purge":
		return ok(Destructive, verb, "PURGE permanently removes dropped objects")
	}
	return unknown(verb)
}

func orName(n, fallback string) string {
	if n == "" {
		return fallback
	}
	return n
}

func orTarget(n string) string { return orName(n, "the target table") }

// with handles WITH queries: data-modifying CTEs and the main statement
// both count.
func with(toks []token, depth int) verdict {
	v := verdict{known: true}
	for _, k := range topLevel(toks) {
		t := toks[k]
		switch {
		case t.kind == tOpen && k > 0 && toks[k-1].is("as", "materialized"):
			v = v.worse(stmt(toks[k+1:closing(toks, k)], depth+1))
		case t.is("select", "insert", "update", "delete", "merge", "values", "table"):
			return stmt(toks[k:], depth+1).worse(v)
		}
	}
	return v.worse(unknown("WITH"))
}

func selectStmt(toks []token) verdict {
	for _, k := range topLevel(toks) {
		if toks[k].is("into") {
			j := k + 1
			for j < len(toks) && toks[j].is("temporary", "temp", "unlogged", "table") {
				j++
			}
			n, _ := name(toks, j)
			return ok(Write, "SELECT INTO", "SELECT INTO creates table "+orName(n, "a new table"))
		}
	}
	return ok(None, "SELECT", "")
}

// updateDelete flags UPDATE and DELETE without a WHERE clause of their own;
// a WHERE inside a subquery (SET x = (SELECT … WHERE …)) does not bound them.
func updateDelete(toks []token, verb string) verdict {
	j := 1
	if verb == "DELETE" {
		j = skip(toks, j, "from")
	}
	j = skip(toks, j, "only")
	bounded := hasWhere(toks)
	target := ""
	if j < len(toks) && toks[j].kind == tOpen { // Oracle inline view
		end := closing(toks, j)
		bounded = bounded || hasWhere(toks[j+1:end])
		target = "a subquery"
	} else {
		target, _ = name(toks, j)
	}
	if !bounded {
		return ok(UnboundedWrite, verb, fmt.Sprintf("%s without WHERE affects every row of %s", verb, orTarget(target)))
	}
	return ok(Write, verb, fmt.Sprintf("%s modifies rows of %s", verb, orTarget(target)))
}

func hasWhere(toks []token) bool {
	for _, k := range topLevel(toks) {
		if toks[k].is("where") {
			return true
		}
	}
	return false
}

// explain is harmless unless it is EXPLAIN ANALYZE, which runs the statement.
func explain(toks []token, depth int) verdict {
	run := false
	j := 1
	if j < len(toks) && toks[j].kind == tOpen {
		end := closing(toks, j)
		opts := toks[j+1 : end]
		for k, t := range opts {
			if t.is("analyze", "analyse") {
				run = k+1 >= len(opts) || !(opts[k+1].is("false", "off") || opts[k+1].raw == "0")
			}
		}
		j = end + 1
	}
	if j > len(toks) {
		j = len(toks)
	}
	for _, k := range topLevel(toks[j:]) {
		t := toks[j+k]
		switch {
		case t.is("analyze", "analyse"):
			run = true
		case t.is("select", "insert", "update", "delete", "merge", "values", "with", "table", "execute", "create", "declare"):
			if !run {
				return ok(None, "EXPLAIN", "")
			}
			return stmt(toks[j+k:], depth+1)
		}
	}
	return ok(None, "EXPLAIN", "")
}

// copyStmt treats COPY … FROM as a write and COPY … TO as a read, except
// that the query of COPY (…) TO may itself be DML with RETURNING.
func copyStmt(toks []token, depth int) verdict {
	v := ok(None, "COPY", "")
	j := 1
	target := ""
	if j < len(toks) && toks[j].kind == tOpen {
		end := closing(toks, j)
		v = v.worse(stmt(toks[j+1:end], depth+1))
		j = min(end+1, len(toks))
	} else {
		target, j = name(toks, j)
	}
	for _, k := range topLevel(toks[j:]) {
		switch t := toks[j+k]; {
		case t.is("from"):
			return v.worse(ok(Write, "COPY FROM", "COPY FROM loads rows into "+orTarget(target)))
		case t.is("to"):
			return v
		}
	}
	return v
}

func create(toks []token) verdict {
	j := 1
	for j < len(toks) && toks[j].is(createModifiers...) {
		j++
	}
	verb := "CREATE"
	if j < len(toks) && toks[j].kind == tWord {
		verb += " " + strings.ToUpper(toks[j].low)
		j++
	}
	j = skip(toks, j, "if", "not", "exists")
	if n, _ := name(toks, j); n != "" {
		return ok(DDL, verb, fmt.Sprintf("%s %s changes the schema", verb, n))
	}
	return ok(DDL, verb, verb+" changes the schema")
}

// objectKind reads the object type after DROP/ALTER ("TABLE",
// "MATERIALIZED VIEW", "PACKAGE BODY", ...) and returns it with the index
// just past it.
func objectKind(toks []token, j int) (string, int) {
	if j >= len(toks) || toks[j].kind != tWord {
		return "", j
	}
	kind := toks[j].low
	j++
	follow := map[string][]string{
		"materialized": {"view"},
		"foreign":      {"table", "data"},
		"data":         {"wrapper"},
		"package":      {"body"},
		"type":         {"body"},
		"public":       {"synonym", "database"},
		"database":     {"link"},
		"event":        {"trigger"},
		"text":         {"search"},
		"search":       {"configuration", "dictionary", "parser", "template"},
		"access":       {"method"},
		"user":         {"mapping"},
		"operator":     {"class", "family"},
		"owned":        {"by"},
	}
	for last := kind; j < len(toks); {
		nexts, ok := follow[last]
		if !ok || !toks[j].is(nexts...) {
			break
		}
		last = toks[j].low
		kind += " " + last
		j++
	}
	return strings.ToUpper(kind), j
}

func drop(toks []token) verdict {
	kind, j := objectKind(toks, 1)
	verb := strings.TrimSpace("DROP " + kind)
	j = skip(toks, j, "concurrently")
	j = skip(toks, j, "if", "exists")
	n, _ := name(toks, j)
	switch {
	case kind == "OWNED BY" && n != "":
		return ok(Destructive, verb, fmt.Sprintf("DROP OWNED BY drops every object %s owns", n))
	case n != "":
		return ok(Destructive, verb, fmt.Sprintf("%s removes %s permanently", verb, n))
	}
	return ok(Destructive, verb, verb+" cannot be undone")
}

func alter(toks []token) verdict {
	kind, j := objectKind(toks, 1)
	verb := strings.TrimSpace("ALTER " + kind)
	if kind == "SESSION" {
		return ok(None, verb, "")
	}
	j = skip(toks, j, "if", "exists")
	j = skip(toks, j, "only")
	n, _ := name(toks, j)
	for _, k := range topLevel(toks) {
		t := toks[k]
		destructive := t.is("truncate") ||
			t.is("drop") && !(k+1 < len(toks) && toks[k+1].is("default", "not", "identity", "expression"))
		if destructive {
			return ok(Destructive, verb, fmt.Sprintf("%s … %s permanently removes part of %s",
				verb, strings.ToUpper(t.low), orName(n, "the object")))
		}
	}
	if n != "" {
		return ok(DDL, verb, fmt.Sprintf("%s changes %s", verb, n))
	}
	return ok(DDL, verb, verb+" changes the schema")
}

// unquote returns the body of a string token.
func unquote(raw string) string {
	if strings.HasPrefix(raw, "$") {
		tag, ok := dollarTag(raw, 0)
		if !ok {
			return ""
		}
		body := raw[len(tag):]
		return strings.TrimSuffix(body, tag)
	}
	if i := strings.IndexByte(raw, '\''); i >= 0 {
		body := strings.TrimSuffix(raw[i+1:], "'")
		return strings.ReplaceAll(body, "''", "'")
	}
	return ""
}
