// Package sqlsafe classifies statements before they run, so the UI can block
// writes on read-only connections and ask for confirmation on risky ones
// against production.
//
// The analysis is lexical and deliberately tolerant: it never fails, it
// ignores strings and comments, and when it cannot tell what a statement does
// it reports it as unknown (Analyze: None, IsWrite: true). WHERE checks are
// syntactic, so "WHERE true" or "WHERE 1=1" still counts as bounded.
package sqlsafe

import "github.com/gxd3v/quarry/internal/driver"

// Risk orders how dangerous a statement is; higher is worse.
type Risk int

const (
	None           Risk = iota
	Write               // modifies data
	DDL                 // changes schema or privileges
	UnboundedWrite      // UPDATE/DELETE without WHERE, Mongo update/delete with an empty filter
	Destructive         // DROP, TRUNCATE, ALTER … DROP, FLUSHALL, drop(), ...
)

func (r Risk) String() string {
	switch r {
	case None:
		return "none"
	case Write:
		return "write"
	case DDL:
		return "ddl"
	case UnboundedWrite:
		return "unbounded write"
	case Destructive:
		return "destructive"
	}
	return "unknown"
}

// Report describes the riskiest statement found. Verb is the statement's
// command as the user would name it ("DELETE", "DROP TABLE", "deleteMany",
// "FLUSHALL"); Reason is a short sentence for confirmation dialogs, empty
// when Risk is None.
type Report struct {
	Risk   Risk
	Verb   string
	Reason string
}

// Analyze classifies stmt, which may hold several statements (the riskiest
// wins). Statements it does not recognise report None.
func Analyze(lang driver.Language, stmt string) Report {
	return analyze(lang, stmt).Report
}

// IsWrite reports whether stmt may modify data or schema. Unrecognised
// statements, procedure calls and anonymous blocks count as writes.
func IsWrite(lang driver.Language, stmt string) bool {
	v := analyze(lang, stmt)
	return v.Risk > None || !v.known
}

func analyze(lang driver.Language, stmt string) verdict {
	switch lang {
	case driver.LangSQL:
		return analyzeSQL(stmt)
	case driver.LangMongo:
		return analyzeMongo(stmt)
	case driver.LangRedis:
		return analyzeRedis(stmt)
	}
	return verdict{}
}

// verdict is a Report plus whether the statement was recognised.
type verdict struct {
	Report
	known bool
}

func ok(r Risk, verb, reason string) verdict {
	return verdict{Report{r, verb, reason}, true}
}

func unknown(verb string) verdict {
	return verdict{Report: Report{Verb: verb}}
}

// worse combines two verdicts: the riskier report wins (the first on ties),
// and the result is known only if both are.
func (a verdict) worse(b verdict) verdict {
	r := a
	if b.Risk > a.Risk || (a.Verb == "" && b.Risk >= a.Risk) {
		r.Report = b.Report
	}
	r.known = a.known && b.known
	return r
}
