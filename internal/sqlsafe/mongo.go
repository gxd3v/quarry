package sqlsafe

import (
	"fmt"
	"strings"
)

type jkind uint8

const (
	jIdent jkind = iota
	jString
	jNumber
	jPunct
	jRegex
)

type jtok struct {
	kind jkind
	text string // strings: the body without quotes
}

func (t jtok) punct(p string) bool { return t.kind == jPunct && t.text == p }

// lexJS tokenizes mongosh input loosely: comments are dropped, strings,
// template literals and regex literals become single tokens.
func lexJS(s string) []jtok {
	var out []jtok
	for i := 0; i < len(s); {
		c := s[i]
		var peek byte
		if i+1 < len(s) {
			peek = s[i+1]
		}
		switch {
		case isSpace(c):
			i++
		case c == '/' && peek == '/':
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(s)
			}
		case c == '/' && peek == '*':
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(s)
			}
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, jtok{jString, s[i+1 : min(j, len(s))]})
			i = min(j+1, len(s))
		case c == '/' && regexAllowed(out):
			j := i + 1
			class := false
			for j < len(s) && s[j] != '\n' && (s[j] != '/' || class) {
				switch s[j] {
				case '\\':
					j++
				case '[':
					class = true
				case ']':
					class = false
				}
				j++
			}
			j = min(j+1, len(s))
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			out = append(out, jtok{jRegex, s[i:j]})
			i = j
		case isIdentStart(c) || c == '$':
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			out = append(out, jtok{jIdent, s[i:j]})
			i = j
		case '0' <= c && c <= '9':
			j := i + 1
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			out = append(out, jtok{jNumber, s[i:j]})
			i = j
		default:
			out = append(out, jtok{jPunct, s[i : i+1]})
			i++
		}
	}
	return out
}

// regexAllowed reports whether a '/' after toks starts a regex literal
// rather than a division.
func regexAllowed(toks []jtok) bool {
	if len(toks) == 0 {
		return true
	}
	t := toks[len(toks)-1]
	return t.kind == jPunct && strings.Contains("(,=:[!&|?{};+-*%<>~^", t.text)
}

// jclose returns the index of the bracket closing the one at i.
func jclose(toks []jtok, i int) int {
	d := 0
	for j := i; j < len(toks); j++ {
		if t := toks[j]; t.kind == jPunct {
			switch t.text {
			case "(", "[", "{":
				d++
			case ")", "]", "}":
				d--
				if d == 0 {
					return j
				}
			}
		}
	}
	return len(toks)
}

// link is one step of a db.… chain: a property or a method call.
type link struct {
	name string
	call bool
	args []jtok
}

func chainAt(toks []jtok, i int) []link {
	var out []link
	for i < len(toks) {
		switch {
		case toks[i].punct(".") && i+1 < len(toks) && toks[i+1].kind == jIdent:
			l := link{name: toks[i+1].text}
			i += 2
			if i < len(toks) && toks[i].punct("(") {
				end := jclose(toks, i)
				l.call, l.args = true, toks[i+1:end]
				i = end + 1
			}
			out = append(out, l)
		case toks[i].punct("["):
			end := jclose(toks, i)
			if end == i+2 && toks[i+1].kind == jString {
				out = append(out, link{name: toks[i+1].text})
			}
			i = end + 1
		default:
			return out
		}
	}
	return out
}

func analyzeMongo(s string) verdict {
	toks := lexJS(s)
	v := verdict{known: true}
	chains := 0
	for i, t := range toks {
		if t.kind != jIdent || t.text != "db" || (i > 0 && toks[i-1].punct(".")) {
			continue
		}
		if c := chainAt(toks, i+1); len(c) > 0 {
			chains++
			v = v.worse(mongoChain(c))
		}
	}
	if chains == 0 && len(toks) > 0 {
		if t := toks[0]; t.kind == jIdent {
			switch t.text {
			case "use", "show", "it", "exit", "quit", "help", "cls", "print", "printjson":
				return ok(None, t.text, "")
			}
			return unknown(t.text)
		}
		return unknown("")
	}
	return v
}

func mongoChain(chain []link) verdict {
	coll := ""
	explain := false
	for _, l := range chain {
		if coll == "" {
			switch {
			case !l.call:
				coll = l.name
			case l.name == "getSiblingDB" || l.name == "getMongo":
			case l.name == "getCollection":
				coll = firstString(l.args)
				if coll == "" {
					coll = "?"
				}
			default:
				return mongoDBMethod(l)
			}
			continue
		}
		switch {
		case !l.call:
			coll += "." + l.name
		case l.name == "explain":
			explain = true
		case explain:
			return ok(None, l.name, "")
		default:
			return mongoCollMethod(coll, l)
		}
	}
	return ok(None, "db", "")
}

var mongoReads = set("find", "findOne", "count", "countDocuments", "estimatedDocumentCount",
	"distinct", "getIndexes", "getIndexKeys", "getIndexSpecs", "indexStats", "stats",
	"dataSize", "storageSize", "totalSize", "totalIndexSize", "isCapped", "latencyStats",
	"watch", "getName", "getFullName", "getDB", "validate", "getShardDistribution",
	"getSearchIndexes", "exists", "analyzeShardKey")

var mongoWrites = set("insert", "insertOne", "insertMany", "save", "update", "updateOne",
	"updateMany", "replaceOne", "remove", "deleteOne", "deleteMany", "bulkWrite",
	"findOneAndUpdate", "findOneAndReplace", "findOneAndDelete", "findAndModify",
	"mapReduce", "initializeOrderedBulkOp", "initializeUnorderedBulkOp")

var mongoCollDDL = set("createIndex", "createIndexes", "ensureIndex", "dropIndex",
	"dropIndexes", "renameCollection", "hideIndex", "unhideIndex", "reIndex",
	"createSearchIndex", "createSearchIndexes", "dropSearchIndex", "updateSearchIndex",
	"compactStructuredEncryptionData", "configureQueryAnalyzer")

func mongoCollMethod(coll string, l link) verdict {
	m := l.name
	switch {
	case m == "drop":
		return ok(Destructive, m, fmt.Sprintf("drop() deletes collection %s and all its documents", coll))
	case m == "aggregate":
		if hasOutStage(l.args) {
			return ok(Write, m, "aggregate with $out or $merge writes to a collection")
		}
		return ok(None, m, "")
	case mongoReads[m]:
		return ok(None, m, "")
	case mongoCollDDL[m]:
		return ok(DDL, m, fmt.Sprintf("%s changes the indexes or definition of %s", m, coll))
	case mongoWrites[m]:
		filtered := strings.HasPrefix(m, "update") || strings.HasPrefix(m, "delete") || m == "remove"
		if filtered && emptyObject(firstArg(l.args)) {
			return ok(UnboundedWrite, m, fmt.Sprintf("%s with an empty filter affects every document in %s", m, coll))
		}
		return ok(Write, m, fmt.Sprintf("%s modifies %s", m, coll))
	}
	return unknown(m)
}

var mongoDBReads = set("getName", "getCollectionNames", "getCollectionInfos", "stats",
	"version", "serverStatus", "serverBuildInfo", "hostInfo", "currentOp", "isMaster",
	"hello", "getUsers", "getUser", "getRoles", "getRole", "getProfilingStatus",
	"printCollectionStats", "listCommands", "getLastError", "getLogComponents",
	"commandHelp", "printShardingStatus", "getReplicationInfo", "printReplicationInfo",
	"printSecondaryReplicationInfo", "watch", "help", "getMongo", "serverCmdLineOpts",
	"getSiblingDB", "getCollection", "aggregate")

var mongoDBDDL = set("createCollection", "createView", "createUser", "dropUser", "updateUser",
	"changeUserPassword", "createRole", "dropRole", "updateRole", "dropAllUsers",
	"dropAllRoles", "grantRolesToUser", "revokeRolesFromUser", "grantPrivilegesToRole",
	"revokePrivilegesFromRole", "grantRolesToRole", "revokeRolesFromRole",
	"setProfilingLevel", "setLogLevel", "createEncryptedCollection")

func mongoDBMethod(l link) verdict {
	m := l.name
	switch {
	case m == "dropDatabase":
		return ok(Destructive, m, "dropDatabase() deletes the current database and all its collections")
	case m == "shutdownServer":
		return ok(Destructive, m, "shutdownServer() stops the server")
	case m == "aggregate" && hasOutStage(l.args):
		return ok(Write, m, "aggregate with $out or $merge writes to a collection")
	case mongoDBReads[m]:
		return ok(None, m, "")
	case mongoDBDDL[m]:
		return ok(DDL, m, m+" changes the database definition or its users")
	case m == "runCommand" || m == "adminCommand":
		return mongoCommand(m, l.args)
	case m == "killOp" || m == "fsyncLock" || m == "fsyncUnlock" || m == "eval":
		return ok(Write, m, m+" changes server state")
	}
	return unknown(m)
}

var mongoCmdReads = set("find", "aggregate", "count", "distinct", "ping", "listCollections",
	"listDatabases", "listIndexes", "serverStatus", "buildInfo", "buildinfo", "hello",
	"isMaster", "ismaster", "dbStats", "dbstats", "collStats", "collstats", "explain",
	"getParameter", "connectionStatus", "hostInfo", "currentOp", "getLog", "whatsmyuri",
	"usersInfo", "rolesInfo", "getCmdLineOpts", "replSetGetStatus", "top", "validate",
	"listCommands", "getMore", "killCursors", "dataSize", "profile")

var mongoCmdWrites = set("insert", "update", "delete", "findAndModify", "mapReduce",
	"killOp", "fsync", "setParameter", "compact", "cloneCollectionAsCapped", "convertToCapped",
	"applyOps")

var mongoCmdDDL = set("create", "createIndexes", "dropIndexes", "collMod", "renameCollection",
	"createUser", "dropUser", "updateUser", "createRole", "dropRole", "updateRole",
	"grantRolesToUser", "revokeRolesFromUser", "dropAllUsersFromDatabase", "reIndex")

func mongoCommand(m string, args []jtok) verdict {
	arg := firstArg(args)
	cmd := ""
	switch {
	case len(arg) > 0 && arg[0].kind == jString:
		cmd = arg[0].text
	case len(arg) > 1 && arg[0].punct("{") && (arg[1].kind == jIdent || arg[1].kind == jString):
		cmd = arg[1].text
	}
	verb := m
	if cmd != "" {
		verb += " " + cmd
	}
	switch {
	case cmd == "drop":
		return ok(Destructive, verb, "drop deletes a collection and all its documents")
	case cmd == "dropDatabase":
		return ok(Destructive, verb, "dropDatabase deletes the database and all its collections")
	case cmd == "shutdown":
		return ok(Destructive, verb, "shutdown stops the server")
	case mongoCmdReads[cmd]:
		return ok(None, verb, "")
	case mongoCmdWrites[cmd]:
		return ok(Write, verb, cmd+" modifies data")
	case mongoCmdDDL[cmd]:
		return ok(DDL, verb, cmd+" changes the database definition")
	}
	return unknown(verb)
}

// firstArg returns the tokens of the first argument of a call.
func firstArg(args []jtok) []jtok {
	d := 0
	for i, t := range args {
		if t.kind != jPunct {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			d++
		case ")", "]", "}":
			d--
		case ",":
			if d == 0 {
				return args[:i]
			}
		}
	}
	return args
}

// emptyObject reports whether arg is {} or missing: both match every document.
func emptyObject(arg []jtok) bool {
	return len(arg) == 0 || len(arg) == 2 && arg[0].punct("{") && arg[1].punct("}")
}

func firstString(args []jtok) string {
	if a := firstArg(args); len(a) == 1 && a[0].kind == jString {
		return a[0].text
	}
	return ""
}

func hasOutStage(args []jtok) bool {
	for i, t := range args {
		if (t.kind == jIdent || t.kind == jString) && (t.text == "$out" || t.text == "$merge") &&
			i+1 < len(args) && args[i+1].punct(":") {
			return true
		}
	}
	return false
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
