package mongo

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/gxd3v/quarry/internal/driver"
)

// run executes a parsed statement:
//
//	use <db>, show dbs | databases | collections | tables
//	db.<coll> or db.getCollection("<name>"), optionally after db.getSiblingDB("<db>"):
//	  find(filter, projection, options) with .sort .limit .skip .projection .hint
//	    .batchSize .maxTimeMS .collation .comment .allowDiskUse .min .max
//	    .returnKey .showRecordId .noCursorTimeout .readConcern .count .size
//	    .itcount .explain .toArray .pretty
//	  findOne, aggregate, countDocuments, count, estimatedDocumentCount,
//	  distinct, insertOne, insertMany, insert, updateOne, updateMany,
//	  replaceOne, deleteOne, deleteMany, remove, findOneAndUpdate,
//	  findOneAndReplace, findOneAndDelete, bulkWrite, createIndex,
//	  createIndexes, dropIndex, dropIndexes, getIndexes, drop, stats,
//	  renameCollection, explain(verbosity).<method>
//	db.runCommand, adminCommand, createCollection, createView,
//	  getCollectionNames, getCollectionInfos, dropDatabase, stats, getName,
//	  version, serverStatus, hostInfo, hello, isMaster, buildInfo,
//	  currentOp, killOp, aggregate
//
// Everything is sent as a database command. In manual-commit mode reads and
// writes of documents join the session's transaction; DDL and runCommand
// run outside it.
func (s *session) run(ctx context.Context, st *statement) (driver.Result, error) {
	switch st.kind {
	case kUse:
		if err := validDBName(st.word); err != nil {
			return nil, errAt(st.wordPos, "%v", err)
		}
		s.db = st.word
		return tagResult("switched to db " + st.word), nil
	case kShow:
		return s.show(ctx, st)
	}
	db := s.db
	if st.hasSibling {
		db = st.sibling
	}
	x := &executor{s: s, db: s.c.client.Database(db), coll: st.coll}
	if st.hasColl {
		return x.collection(ctx, st.calls)
	}
	return x.database(ctx, st.calls)
}

func validDBName(name string) error {
	if name == "" || len(name) > 63 || strings.ContainsAny(name, "/\\. \"$*<>:|?\x00") {
		return fmt.Errorf("invalid database name %q", name)
	}
	return nil
}

// executor runs the methods of one statement.
type executor struct {
	s    *session
	db   *mongo.Database
	coll string
}

func (x *executor) command(ctx context.Context, txable bool, cmd bson.D) (bson.Raw, error) {
	octx, err := x.s.opCtx(ctx, txable)
	if err != nil {
		return nil, err
	}
	return x.db.RunCommand(octx, cmd).Raw()
}

func (x *executor) cursor(ctx context.Context, txable bool, cmd bson.D) (*docResult, error) {
	octx, err := x.s.opCtx(ctx, txable)
	if err != nil {
		return nil, err
	}
	cur, err := x.db.RunCommandCursor(octx, cmd)
	if err != nil {
		return nil, err
	}
	return newCursorResult(octx, x.s, cur)
}

// reply runs cmd outside any transaction and returns its reply as a
// one-document result.
func (x *executor) reply(ctx context.Context, cmd bson.D) (driver.Result, error) {
	raw, err := x.command(ctx, false, cmd)
	if err != nil {
		return nil, err
	}
	return newDocsResult(stripReply(raw)), nil
}

// queryKind says how the reply of an explainable command becomes a result.
type queryKind uint8

const (
	qCursor queryKind = iota
	qCount
	qDistinct
	qUpdate
	qDelete
	qModify
)

// query is an explainable command built from a method call.
type query struct {
	kind    queryKind
	cmd     bson.D
	txable  bool
	explain string // verbosity asked by an .explain() modifier
	field   string // distinct key
}

func (x *executor) collection(ctx context.Context, calls []call) (driver.Result, error) {
	verbosity := ""
	if calls[0].name == "explain" {
		v, err := explainVerbosity(calls[0])
		if err != nil {
			return nil, err
		}
		if len(calls) < 2 {
			return nil, errAt(calls[0].end, "explain() must be followed by a method, e.g. .find()")
		}
		verbosity, calls = v, calls[1:]
	}
	m, mods := calls[0], calls[1:]
	q, ok, err := x.build(m, mods)
	if err != nil {
		return nil, err
	}
	if ok {
		if v := cmp.Or(verbosity, q.explain); v != "" {
			return x.reply(ctx, bson.D{{Key: "explain", Value: q.cmd}, {Key: "verbosity", Value: v}})
		}
		return x.runQuery(ctx, q)
	}
	if verbosity != "" {
		return nil, errAt(m.pos, "%s cannot be explained", m.name)
	}
	if len(mods) > 0 && !onlyNoOps(mods) {
		return nil, errAt(mods[0].pos, "%s does not return a cursor: .%s() is not supported", m.name, mods[0].name)
	}
	return x.collMethod(ctx, m)
}

// build turns an explainable method into a query; ok is false for other
// methods.
func (x *executor) build(m call, mods []call) (*query, bool, error) {
	var (
		q   *query
		err error
	)
	switch m.name {
	case "find":
		q, err = x.find(m, mods, false)
	case "findOne":
		q, err = x.find(m, mods, true)
	case "aggregate":
		q, err = aggregate(m, mods, x.coll)
	case "countDocuments", "count":
		q, err = x.countDocuments(m)
	case "distinct":
		q, err = x.distinct(m)
	case "updateOne", "updateMany", "replaceOne":
		q, err = x.update(m)
	case "deleteOne", "deleteMany", "remove":
		q, err = x.delete(m)
	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete":
		q, err = x.findAndModify(m)
	default:
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	switch m.name {
	case "find", "findOne", "aggregate":
	default:
		if len(mods) > 0 {
			switch {
			case len(mods) == 1 && mods[0].name == "explain":
				if q.explain, err = explainVerbosity(mods[0]); err != nil {
					return nil, true, err
				}
			case !onlyNoOps(mods):
				return nil, true, errAt(mods[0].pos, "%s does not return a cursor: .%s() is not supported", m.name, mods[0].name)
			}
		}
	}
	return q, true, nil
}

func onlyNoOps(mods []call) bool {
	for _, m := range mods {
		if m.name != "toArray" && m.name != "pretty" {
			return false
		}
	}
	return true
}

func explainVerbosity(c call) (string, error) {
	v, ok := c.arg(0)
	if !ok {
		return "queryPlanner", nil
	}
	switch v := v.(type) {
	case bool:
		if v {
			return "allPlansExecution", nil
		}
		return "queryPlanner", nil
	case string:
		switch v {
		case "queryPlanner", "executionStats", "allPlansExecution":
			return v, nil
		}
	}
	return "", errAt(c.argAt(0), "explain: verbosity must be \"queryPlanner\", \"executionStats\" or \"allPlansExecution\"")
}

func (x *executor) runQuery(ctx context.Context, q *query) (driver.Result, error) {
	switch q.kind {
	case qCursor:
		return x.cursor(ctx, q.txable, q.cmd)
	case qCount:
		r, err := x.cursor(ctx, q.txable, q.cmd)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		var n int64
		if len(r.docs) > 0 {
			n = r.docs[0].Lookup("n").AsInt64()
		}
		return singleRow(-1, "count", n), nil
	case qDistinct:
		raw, err := x.command(ctx, true, q.cmd)
		if err != nil {
			return nil, err
		}
		vals, _ := raw.Lookup("values").Array().Values()
		out := make([]any, len(vals))
		for i, v := range vals {
			out[i] = normalize(v)
		}
		return column(q.field, out), nil
	case qUpdate:
		r, err := x.write(ctx, q.cmd)
		if err != nil {
			return nil, err
		}
		var upserted any
		if len(r.Upserted) > 0 {
			upserted = normalize(r.Upserted[0].ID)
		}
		up := int64(len(r.Upserted))
		return singleRow(r.N, "matchedCount", r.N-up, "modifiedCount", r.NModified, "upsertedCount", up, "upsertedId", upserted), nil
	case qDelete:
		r, err := x.write(ctx, q.cmd)
		if err != nil {
			return nil, err
		}
		return singleRow(r.N, "deletedCount", r.N), nil
	case qModify:
		raw, err := x.command(ctx, true, q.cmd)
		if err != nil {
			return nil, err
		}
		var res *docResult
		if v, err := raw.LookupErr("value"); err == nil && v.Type == bson.TypeEmbeddedDocument {
			res = newDocsResult(v.Document())
		} else {
			res = newDocsResult()
		}
		res.affected = 0
		if n, err := raw.LookupErr("lastErrorObject", "n"); err == nil {
			res.affected = n.AsInt64()
		}
		return res, nil
	}
	return nil, fmt.Errorf("unknown query kind %d", q.kind)
}

// ---------------------------------------------------------------------------
// Reads

// findFields maps find options and cursor modifiers to find command
// fields; flags take no value and default to true.
var findFields = map[string]string{
	"sort": "sort", "limit": "limit", "skip": "skip", "projection": "projection", "project": "projection",
	"hint": "hint", "batchSize": "batchSize", "maxTimeMS": "maxTimeMS", "collation": "collation",
	"comment": "comment", "allowDiskUse": "allowDiskUse", "min": "min", "max": "max",
	"returnKey": "returnKey", "showRecordId": "showRecordId", "noCursorTimeout": "noCursorTimeout",
	"readConcern": "readConcern", "let": "let", "singleBatch": "singleBatch", "maxAwaitTimeMS": "maxAwaitTimeMS",
}

var findFlags = map[string]bool{
	"allowDiskUse": true, "returnKey": true, "showRecordId": true, "noCursorTimeout": true, "singleBatch": true,
}

func (x *executor) find(m call, mods []call, one bool) (*query, error) {
	if err := maxArgs(m, 3); err != nil {
		return nil, err
	}
	filter, err := docArg(m, 0, "filter", true)
	if err != nil {
		return nil, err
	}
	proj, err := docArg(m, 1, "projection", true)
	if err != nil {
		return nil, err
	}
	opts, err := docArg(m, 2, "options", true)
	if err != nil {
		return nil, err
	}
	cmd := bson.D{{Key: "find", Value: x.coll}, {Key: "filter", Value: filter}}
	if len(proj) > 0 {
		set(&cmd, "projection", proj)
	}
	for _, e := range opts {
		if err := findField(&cmd, e.Key, e.Value); err != nil {
			return nil, errAt(m.argAt(2), "%s: %v", m.name, err)
		}
	}
	q := &query{kind: qCursor, txable: true}
	count := ""
	for _, md := range mods {
		switch md.name {
		case "toArray", "pretty":
			if err := maxArgs(md, 0); err != nil {
				return nil, err
			}
		case "explain":
			if q.explain, err = explainVerbosity(md); err != nil {
				return nil, err
			}
		case "count", "size", "itcount":
			count = md.name
		default:
			if _, ok := findFields[md.name]; !ok || one {
				return nil, errAt(md.pos, "unsupported cursor method %s", md.name)
			}
			if err := maxArgs(md, 1); err != nil {
				return nil, err
			}
			v, ok := md.arg(0)
			if !ok {
				if !findFlags[md.name] {
					return nil, errAt(md.end-1, "%s: missing argument", md.name)
				}
				v = true
			}
			if err := findField(&cmd, md.name, v); err != nil {
				return nil, errAt(md.argAt(0), "%s: %v", md.name, err)
			}
		}
	}
	if one {
		set(&cmd, "limit", int64(1))
		set(&cmd, "singleBatch", true)
	}
	if count != "" {
		q.kind, q.cmd = qCount, countPipeline(x.coll, cmd, count != "count")
		return q, nil
	}
	if !has(cmd, "batchSize") {
		set(&cmd, "batchSize", int32(batchSize))
	}
	q.cmd = cmd
	return q, nil
}

func findField(cmd *bson.D, name string, v any) error {
	field, ok := findFields[name]
	if !ok {
		return fmt.Errorf("unknown option %q", name)
	}
	switch field {
	case "limit", "skip", "batchSize", "maxTimeMS", "maxAwaitTimeMS":
		n, ok := intValue(v)
		if !ok {
			return fmt.Errorf("%s must be an integer", name)
		}
		if field == "limit" && n < 0 {
			n = -n
			set(cmd, "singleBatch", true)
		}
		if n < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
		if field == "limit" && n == 0 {
			return nil
		}
		v = n
	case "sort", "projection", "collation", "min", "max", "let":
		if _, ok := v.(bson.D); !ok {
			return fmt.Errorf("%s must be a document", name)
		}
	case "readConcern":
		if s, ok := v.(string); ok {
			v = bson.D{{Key: "level", Value: s}}
		}
	}
	set(cmd, field, v)
	return nil
}

// countPipeline counts the documents a find command matches with an
// aggregation, like countDocuments does; withSkip also applies its skip and
// limit.
func countPipeline(coll string, find bson.D, withSkip bool) bson.D {
	pipeline := bson.A{bson.D{{Key: "$match", Value: get(find, "filter")}}}
	if withSkip {
		if v := get(find, "skip"); v != nil {
			pipeline = append(pipeline, bson.D{{Key: "$skip", Value: v}})
		}
		if v := get(find, "limit"); v != nil {
			pipeline = append(pipeline, bson.D{{Key: "$limit", Value: v}})
		}
	}
	pipeline = append(pipeline, bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: 1}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}})
	cmd := bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: bson.D{}}}
	for _, k := range []string{"hint", "maxTimeMS", "collation", "comment", "readConcern"} {
		if v := get(find, k); v != nil {
			cmd = append(cmd, bson.E{Key: k, Value: v})
		}
	}
	return cmd
}

func (x *executor) countDocuments(m call) (*query, error) {
	if err := maxArgs(m, 2); err != nil {
		return nil, err
	}
	filter, err := docArg(m, 0, "filter", true)
	if err != nil {
		return nil, err
	}
	opts, err := docArg(m, 1, "options", true)
	if err != nil {
		return nil, err
	}
	find := bson.D{{Key: "filter", Value: filter}}
	for _, e := range opts {
		switch e.Key {
		case "skip", "limit", "hint", "maxTimeMS", "collation", "comment", "readConcern":
			if err := findField(&find, e.Key, e.Value); err != nil {
				return nil, errAt(m.argAt(1), "%s: %v", m.name, err)
			}
		default:
			return nil, errAt(m.argAt(1), "%s: unknown option %q", m.name, e.Key)
		}
	}
	return &query{kind: qCount, txable: true, cmd: countPipeline(x.coll, find, true)}, nil
}

func (x *executor) distinct(m call) (*query, error) {
	if err := maxArgs(m, 3); err != nil {
		return nil, err
	}
	field, err := stringArg(m, 0, "field name")
	if err != nil {
		return nil, err
	}
	filter, err := docArg(m, 1, "filter", true)
	if err != nil {
		return nil, err
	}
	opts, err := docArg(m, 2, "options", true)
	if err != nil {
		return nil, err
	}
	cmd := bson.D{{Key: "distinct", Value: x.coll}, {Key: "key", Value: field}, {Key: "query", Value: filter}}
	cmd = append(cmd, opts...)
	return &query{kind: qDistinct, txable: true, cmd: cmd, field: field}, nil
}

// aggregate builds an aggregation on coll (a name, or 1 for db.aggregate).
// The pipeline is an array, or mongosh-style stages as separate arguments.
func aggregate(m call, mods []call, coll any) (*query, error) {
	var (
		pipeline bson.A
		opts     bson.D
	)
	switch first, _ := m.arg(0); first.(type) {
	case nil, bson.A:
		if err := maxArgs(m, 2); err != nil {
			return nil, err
		}
		pipeline, _ = first.(bson.A)
		var err error
		if opts, err = docArg(m, 1, "options", true); err != nil {
			return nil, err
		}
	case bson.D:
		pipeline = bson.A(m.args)
	default:
		return nil, errAt(m.argAt(0), "aggregate: the pipeline must be an array of stages")
	}
	if pipeline == nil {
		pipeline = bson.A{}
	}
	for i, st := range pipeline {
		if d, ok := st.(bson.D); !ok || len(d) != 1 {
			pos := m.argAt(0)
			if len(m.args) > 1 && i < len(m.argPos) {
				pos = m.argAt(i)
			}
			return nil, errAt(pos, "aggregate: stage %d must be a document with a single $stage field", i+1)
		}
	}
	cursor := bson.D{{Key: "batchSize", Value: int32(batchSize)}}
	cmd := bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: pipeline}}
	q := &query{kind: qCursor, txable: !writesOut(pipeline)}
	for _, e := range opts {
		switch e.Key {
		case "batchSize":
			cursor = bson.D{{Key: "batchSize", Value: e.Value}}
		case "explain":
			if b, _ := e.Value.(bool); b {
				q.explain = "queryPlanner"
			}
		default:
			cmd = append(cmd, e)
		}
	}
	cmd = append(cmd, bson.E{Key: "cursor", Value: cursor})
	for _, md := range mods {
		switch md.name {
		case "toArray", "pretty":
		case "explain":
			v, err := explainVerbosity(md)
			if err != nil {
				return nil, err
			}
			q.explain = v
		default:
			return nil, errAt(md.pos, "unsupported cursor method %s", md.name)
		}
	}
	q.cmd = cmd
	return q, nil
}

// writesOut reports whether a pipeline ends with $out or $merge, which
// cannot run in a transaction.
func writesOut(pipeline bson.A) bool {
	if len(pipeline) == 0 {
		return false
	}
	d, ok := pipeline[len(pipeline)-1].(bson.D)
	return ok && len(d) == 1 && (d[0].Key == "$out" || d[0].Key == "$merge")
}

// ---------------------------------------------------------------------------
// Writes

// writeReply is the reply of the insert, update and delete commands.
type writeReply struct {
	N         int64 `bson:"n"`
	NModified int64 `bson:"nModified"`
	Upserted  []struct {
		Index int           `bson:"index"`
		ID    bson.RawValue `bson:"_id"`
	} `bson:"upserted"`
	WriteErrors []struct {
		Index    int      `bson:"index"`
		Code     int      `bson:"code"`
		CodeName string   `bson:"codeName"`
		Msg      string   `bson:"errmsg"`
		Info     bson.Raw `bson:"errInfo"`
	} `bson:"writeErrors"`
	WCError *struct {
		Code     int    `bson:"code"`
		CodeName string `bson:"codeName"`
		Msg      string `bson:"errmsg"`
	} `bson:"writeConcernError"`
}

func (r *writeReply) err() error {
	if len(r.WriteErrors) > 0 {
		w := r.WriteErrors[0]
		qe := &driver.QueryError{Msg: w.Msg, Code: codeName(w.Code, w.CodeName), Position: -1}
		if len(w.Info) > 0 {
			qe.Detail = extJSON(w.Info)
		}
		var hints []string
		if len(r.WriteErrors) > 1 {
			hints = append(hints, fmt.Sprintf("%d writes failed; the first, at index %d, is shown", len(r.WriteErrors), w.Index))
		}
		if r.N > 0 {
			hints = append(hints, fmt.Sprintf("%d documents were written", r.N))
		}
		qe.Hint = strings.Join(hints, "; ")
		return qe
	}
	if r.WCError != nil {
		return &driver.QueryError{Msg: "write concern error: " + r.WCError.Msg, Code: codeName(r.WCError.Code, r.WCError.CodeName), Position: -1}
	}
	return nil
}

// write runs an insert, update or delete command; write errors become a
// *driver.QueryError.
func (x *executor) write(ctx context.Context, cmd bson.D) (*writeReply, error) {
	raw, err := x.command(ctx, true, cmd)
	if err != nil {
		return nil, err
	}
	var r writeReply
	if err := bson.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, r.err()
}

func (x *executor) insert(ctx context.Context, m call) (driver.Result, error) {
	if err := maxArgs(m, 2); err != nil {
		return nil, err
	}
	v, ok := m.arg(0)
	if !ok {
		return nil, errAt(m.argAt(0), "%s: missing document", m.name)
	}
	var docs bson.A
	switch v := v.(type) {
	case bson.D:
		if m.name == "insertMany" {
			return nil, errAt(m.argAt(0), "insertMany: expected an array of documents")
		}
		docs = bson.A{v}
	case bson.A:
		if m.name == "insertOne" {
			return nil, errAt(m.argAt(0), "insertOne: expected a document (use insertMany for an array)")
		}
		docs = v
	default:
		return nil, errAt(m.argAt(0), "%s: expected a document", m.name)
	}
	opts, err := docArg(m, 1, "options", true)
	if err != nil {
		return nil, err
	}
	ids := make([]any, len(docs))
	for i, d := range docs {
		doc, ok := d.(bson.D)
		if !ok {
			return nil, errAt(m.argAt(0), "%s: element %d is not a document", m.name, i+1)
		}
		docs[i], ids[i] = withID(doc)
	}
	cmd := bson.D{{Key: "insert", Value: x.coll}, {Key: "documents", Value: docs}}
	cmd = append(cmd, opts...)
	r, err := x.write(ctx, cmd)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = normalizeGo(id)
	}
	res := column("insertedId", out)
	res.affected = r.N
	return res, nil
}

// withID returns doc with an _id (a new ObjectId when it has none, put
// first like the shell does) and that _id.
func withID(doc bson.D) (bson.D, any) {
	if i := slices.IndexFunc(doc, func(e bson.E) bool { return e.Key == "_id" }); i >= 0 {
		return doc, doc[i].Value
	}
	id := bson.NewObjectID()
	return append(bson.D{{Key: "_id", Value: id}}, doc...), id
}

// updateSpec builds one element of an update command's updates array.
func updateSpec(filter bson.D, u any, opts bson.D, multi, replace bool) (bson.D, error) {
	switch u := u.(type) {
	case bson.D:
		ops := len(u) > 0 && strings.HasPrefix(u[0].Key, "$")
		for _, e := range u {
			if strings.HasPrefix(e.Key, "$") != ops {
				return nil, fmt.Errorf("the update mixes operators and fields")
			}
		}
		switch {
		case replace && ops:
			return nil, fmt.Errorf("the replacement must not contain update operators")
		case !replace && !ops:
			return nil, fmt.Errorf("the update must use operators like $set (use replaceOne to replace a document)")
		}
	case bson.A:
		if replace {
			return nil, fmt.Errorf("the replacement must be a document")
		}
	default:
		return nil, fmt.Errorf("the update must be a document or a pipeline")
	}
	spec := bson.D{{Key: "q", Value: filter}, {Key: "u", Value: u}, {Key: "multi", Value: multi}}
	for _, e := range opts {
		switch e.Key {
		case "upsert", "arrayFilters", "hint", "collation":
			spec = append(spec, e)
		default:
			return nil, fmt.Errorf("unknown option %q", e.Key)
		}
	}
	return spec, nil
}

// splitOptions separates command-level write options from per-statement
// ones.
func splitOptions(opts bson.D) (stmt, cmd bson.D) {
	for _, e := range opts {
		switch e.Key {
		case "writeConcern", "bypassDocumentValidation", "comment", "let", "ordered":
			cmd = append(cmd, e)
		default:
			stmt = append(stmt, e)
		}
	}
	return stmt, cmd
}

func (x *executor) update(m call) (*query, error) {
	if err := maxArgs(m, 3); err != nil {
		return nil, err
	}
	filter, err := docArg(m, 0, "filter", false)
	if err != nil {
		return nil, err
	}
	u, ok := m.arg(1)
	if !ok {
		return nil, errAt(m.argAt(1), "%s: missing update", m.name)
	}
	opts, err := docArg(m, 2, "options", true)
	if err != nil {
		return nil, err
	}
	stmtOpts, cmdOpts := splitOptions(opts)
	spec, err := updateSpec(filter, u, stmtOpts, m.name == "updateMany", m.name == "replaceOne")
	if err != nil {
		return nil, errAt(m.argAt(1), "%s: %v", m.name, err)
	}
	cmd := bson.D{{Key: "update", Value: x.coll}, {Key: "updates", Value: bson.A{spec}}}
	return &query{kind: qUpdate, txable: true, cmd: append(cmd, cmdOpts...)}, nil
}

func (x *executor) delete(m call) (*query, error) {
	if err := maxArgs(m, 2); err != nil {
		return nil, err
	}
	filter, err := docArg(m, 0, "filter", m.name == "remove")
	if err != nil {
		return nil, err
	}
	limit := int32(0)
	if m.name == "deleteOne" {
		limit = 1
	}
	var opts bson.D
	switch v, _ := m.arg(1); v := v.(type) {
	case nil:
	case bool: // remove(filter, justOne)
		if m.name != "remove" {
			return nil, errAt(m.argAt(1), "%s: options must be a document", m.name)
		}
		if v {
			limit = 1
		}
	case bson.D:
		opts = v
	default:
		return nil, errAt(m.argAt(1), "%s: options must be a document", m.name)
	}
	spec := bson.D{{Key: "q", Value: filter}, {Key: "limit", Value: limit}}
	stmtOpts, cmdOpts := splitOptions(opts)
	for _, e := range stmtOpts {
		switch e.Key {
		case "hint", "collation":
			spec = append(spec, e)
		case "justOne":
			if b, _ := e.Value.(bool); b && m.name == "remove" {
				spec[1].Value = int32(1)
			}
		default:
			return nil, errAt(m.argAt(1), "%s: unknown option %q", m.name, e.Key)
		}
	}
	cmd := bson.D{{Key: "delete", Value: x.coll}, {Key: "deletes", Value: bson.A{spec}}}
	return &query{kind: qDelete, txable: true, cmd: append(cmd, cmdOpts...)}, nil
}

func (x *executor) findAndModify(m call) (*query, error) {
	del := m.name == "findOneAndDelete"
	nargs := 3
	if del {
		nargs = 2
	}
	if err := maxArgs(m, nargs); err != nil {
		return nil, err
	}
	filter, err := docArg(m, 0, "filter", false)
	if err != nil {
		return nil, err
	}
	cmd := bson.D{{Key: "findAndModify", Value: x.coll}, {Key: "query", Value: filter}}
	optArg := 1
	if del {
		cmd = append(cmd, bson.E{Key: "remove", Value: true})
	} else {
		u, ok := m.arg(1)
		if !ok {
			return nil, errAt(m.argAt(1), "%s: missing update", m.name)
		}
		if _, err := updateSpec(nil, u, nil, false, m.name == "findOneAndReplace"); err != nil {
			return nil, errAt(m.argAt(1), "%s: %v", m.name, err)
		}
		cmd = append(cmd, bson.E{Key: "update", Value: u})
		optArg = 2
	}
	opts, err := docArg(m, optArg, "options", true)
	if err != nil {
		return nil, err
	}
	for _, e := range opts {
		switch e.Key {
		case "projection":
			cmd = append(cmd, bson.E{Key: "fields", Value: e.Value})
		case "returnDocument":
			cmd = append(cmd, bson.E{Key: "new", Value: e.Value == "after"})
		case "returnNewDocument":
			cmd = append(cmd, bson.E{Key: "new", Value: e.Value})
		case "sort", "upsert", "arrayFilters", "collation", "hint", "maxTimeMS", "let", "comment",
			"bypassDocumentValidation", "writeConcern":
			cmd = append(cmd, e)
		default:
			return nil, errAt(m.argAt(optArg), "%s: unknown option %q", m.name, e.Key)
		}
	}
	return &query{kind: qModify, txable: true, cmd: cmd}, nil
}

// bulkWrite runs each operation as its own command, in order. Ordered
// (the default) stops at the first failure; unordered runs every
// operation and reports the first failure at the end. Either way earlier
// writes stay applied unless the session is in a transaction.
func (x *executor) bulkWrite(ctx context.Context, m call) (driver.Result, error) {
	if err := maxArgs(m, 2); err != nil {
		return nil, err
	}
	arg, _ := m.arg(0)
	ops, ok := arg.(bson.A)
	if !ok {
		return nil, errAt(m.argAt(0), "bulkWrite: expected an array of operations")
	}
	opts, err := docArg(m, 1, "options", true)
	if err != nil {
		return nil, err
	}
	ordered := true
	var cmdOpts bson.D
	for _, e := range opts {
		switch e.Key {
		case "ordered":
			ordered, _ = e.Value.(bool)
		case "writeConcern", "bypassDocumentValidation", "comment", "let":
			cmdOpts = append(cmdOpts, e)
		default:
			return nil, errAt(m.argAt(1), "bulkWrite: unknown option %q", e.Key)
		}
	}
	cmds := make([]bson.D, len(ops))
	kinds := make([]string, len(ops))
	for i, op := range ops {
		cmd, kind, err := x.bulkOp(op)
		if err != nil {
			return nil, errAt(m.argAt(0), "bulkWrite: operation %d: %v", i+1, err)
		}
		cmds[i], kinds[i] = append(cmd, cmdOpts...), kind
	}
	var ins, matched, modified, deleted, upserted int64
	var first error
	for i, cmd := range cmds {
		r, err := x.write(ctx, cmd)
		if err != nil {
			if qe, ok := err.(*driver.QueryError); ok {
				q := *qe
				q.Msg = fmt.Sprintf("operation %d (%s): %s", i+1, kinds[i], q.Msg)
				err = &q
			}
			if ordered {
				return nil, err
			}
			first = cmp.Or(first, err)
			continue
		}
		switch kinds[i] {
		case "insertOne":
			ins += r.N
		case "deleteOne", "deleteMany":
			deleted += r.N
		default:
			up := int64(len(r.Upserted))
			matched += r.N - up
			modified += r.NModified
			upserted += up
		}
	}
	if first != nil {
		return nil, first
	}
	return singleRow(ins+matched+deleted+upserted, "insertedCount", ins, "matchedCount", matched,
		"modifiedCount", modified, "deletedCount", deleted, "upsertedCount", upserted), nil
}

func (x *executor) bulkOp(op any) (bson.D, string, error) {
	d, ok := op.(bson.D)
	if !ok || len(d) != 1 {
		return nil, "", fmt.Errorf("expected { <operation>: { ... } }")
	}
	kind := d[0].Key
	args, ok := d[0].Value.(bson.D)
	if !ok {
		return nil, "", fmt.Errorf("%s: expected a document", kind)
	}
	filter, _ := get(args, "filter").(bson.D)
	var opts bson.D
	for _, e := range args {
		switch e.Key {
		case "filter", "update", "replacement", "document":
		default:
			opts = append(opts, e)
		}
	}
	switch kind {
	case "insertOne":
		doc, ok := get(args, "document").(bson.D)
		if !ok {
			return nil, "", fmt.Errorf("insertOne needs a document")
		}
		doc, _ = withID(doc)
		return bson.D{{Key: "insert", Value: x.coll}, {Key: "documents", Value: bson.A{doc}}}, kind, nil
	case "updateOne", "updateMany", "replaceOne":
		u := get(args, "update")
		if kind == "replaceOne" {
			u = get(args, "replacement")
		}
		spec, err := updateSpec(orEmpty(filter), u, opts, kind == "updateMany", kind == "replaceOne")
		if err != nil {
			return nil, "", fmt.Errorf("%s: %v", kind, err)
		}
		return bson.D{{Key: "update", Value: x.coll}, {Key: "updates", Value: bson.A{spec}}}, kind, nil
	case "deleteOne", "deleteMany":
		limit := int32(0)
		if kind == "deleteOne" {
			limit = 1
		}
		spec := bson.D{{Key: "q", Value: orEmpty(filter)}, {Key: "limit", Value: limit}}
		spec = append(spec, opts...)
		return bson.D{{Key: "delete", Value: x.coll}, {Key: "deletes", Value: bson.A{spec}}}, kind, nil
	}
	return nil, "", fmt.Errorf("unknown operation %q", kind)
}

// ---------------------------------------------------------------------------
// Other collection methods

func (x *executor) collMethod(ctx context.Context, m call) (driver.Result, error) {
	switch m.name {
	case "insertOne", "insertMany", "insert":
		return x.insert(ctx, m)
	case "bulkWrite":
		return x.bulkWrite(ctx, m)
	case "estimatedDocumentCount":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		opts, err := docArg(m, 0, "options", true)
		if err != nil {
			return nil, err
		}
		raw, err := x.command(ctx, false, append(bson.D{{Key: "count", Value: x.coll}}, opts...))
		if err != nil {
			return nil, err
		}
		return singleRow(-1, "count", raw.Lookup("n").AsInt64()), nil
	case "createIndex", "createIndexes":
		return x.createIndexes(ctx, m)
	case "dropIndex", "dropIndexes":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		idx, ok := m.arg(0)
		if !ok {
			if m.name == "dropIndex" {
				return nil, errAt(m.argAt(0), "dropIndex: missing index name or key pattern")
			}
			idx = "*"
		}
		if _, err := x.command(ctx, false, bson.D{{Key: "dropIndexes", Value: x.coll}, {Key: "index", Value: idx}}); err != nil {
			return nil, err
		}
		if s, ok := idx.(string); ok && s == "*" {
			return tagResult("dropped the indexes of " + x.coll), nil
		}
		return tagResult("dropped index " + formatJS(idx)), nil
	case "getIndexes", "getIndexSpecs":
		if err := maxArgs(m, 0); err != nil {
			return nil, err
		}
		return x.cursor(ctx, false, bson.D{{Key: "listIndexes", Value: x.coll}})
	case "drop":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		if _, err := x.command(ctx, false, bson.D{{Key: "drop", Value: x.coll}}); err != nil {
			return nil, err
		}
		return tagResult("dropped collection " + x.coll), nil
	case "stats":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		return x.cursor(ctx, false, bson.D{
			{Key: "aggregate", Value: x.coll},
			{Key: "pipeline", Value: bson.A{bson.D{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}}}}}},
			{Key: "cursor", Value: bson.D{}},
		})
	case "renameCollection":
		if err := maxArgs(m, 2); err != nil {
			return nil, err
		}
		to, err := stringArg(m, 0, "new name")
		if err != nil {
			return nil, err
		}
		drop, _ := m.arg(1)
		dropTarget, _ := drop.(bool)
		ns := x.db.Name() + "."
		cmd := bson.D{{Key: "renameCollection", Value: ns + x.coll}, {Key: "to", Value: ns + to}, {Key: "dropTarget", Value: dropTarget}}
		if _, err := (&executor{s: x.s, db: x.s.c.client.Database("admin")}).command(ctx, false, cmd); err != nil {
			return nil, err
		}
		return tagResult("renamed collection " + x.coll + " to " + to), nil
	}
	return nil, errAt(m.pos, "unknown or unsupported collection method %s", m.name)
}

func (x *executor) createIndexes(ctx context.Context, m call) (driver.Result, error) {
	if err := maxArgs(m, 3); err != nil {
		return nil, err
	}
	var keys bson.A
	switch v, _ := m.arg(0); v := v.(type) {
	case bson.D:
		if m.name == "createIndexes" {
			return nil, errAt(m.argAt(0), "createIndexes: expected an array of key patterns")
		}
		keys = bson.A{v}
	case bson.A:
		if m.name == "createIndex" {
			return nil, errAt(m.argAt(0), "createIndex: expected a key pattern document")
		}
		keys = v
	default:
		return nil, errAt(m.argAt(0), "%s: expected a key pattern like { field: 1 }", m.name)
	}
	opts, err := docArg(m, 1, "options", true)
	if err != nil {
		return nil, err
	}
	specs := make(bson.A, len(keys))
	names := make([]any, len(keys))
	for i, k := range keys {
		kd, ok := k.(bson.D)
		if !ok || len(kd) == 0 {
			return nil, errAt(m.argAt(0), "%s: key pattern %d must be a non-empty document", m.name, i+1)
		}
		spec := bson.D{{Key: "key", Value: kd}}
		name, _ := get(opts, "name").(string)
		if name == "" || len(keys) > 1 {
			name = indexName(kd)
		}
		spec = append(spec, bson.E{Key: "name", Value: name})
		for _, e := range opts {
			if e.Key != "name" {
				spec = append(spec, e)
			}
		}
		specs[i], names[i] = spec, name
	}
	cmd := bson.D{{Key: "createIndexes", Value: x.coll}, {Key: "indexes", Value: specs}}
	if v, ok := m.arg(2); ok {
		cmd = append(cmd, bson.E{Key: "commitQuorum", Value: v})
	}
	if _, err := x.command(ctx, false, cmd); err != nil {
		return nil, err
	}
	return column("name", names), nil
}

// indexName is the default name of an index: field_value pairs joined
// with "_", e.g. a_1_b_-1.
func indexName(keys bson.D) string {
	parts := make([]string, 0, 2*len(keys))
	for _, e := range keys {
		v := fmt.Sprint(e.Value)
		if n, ok := intValue(e.Value); ok {
			v = strconv.FormatInt(n, 10)
		}
		parts = append(parts, e.Key, v)
	}
	return strings.Join(parts, "_")
}

// ---------------------------------------------------------------------------
// Database methods

// cursorCommands return a cursor when run with runCommand.
var cursorCommands = map[string]bool{"find": true, "aggregate": true, "listCollections": true, "listIndexes": true}

// simpleCommands map shell helpers to the command they run.
var simpleCommands = map[string]string{
	"serverStatus": "serverStatus", "hostInfo": "hostInfo", "hello": "hello", "isMaster": "hello",
	"buildInfo": "buildInfo", "serverBuildInfo": "buildInfo", "listCommands": "listCommands",
}

func (x *executor) database(ctx context.Context, calls []call) (driver.Result, error) {
	m := calls[0]
	if m.name == "aggregate" {
		q, err := aggregate(m, calls[1:], int32(1))
		if err != nil {
			return nil, err
		}
		if q.explain != "" {
			return x.reply(ctx, bson.D{{Key: "explain", Value: q.cmd}, {Key: "verbosity", Value: q.explain}})
		}
		return x.runQuery(ctx, q)
	}
	if len(calls) > 1 && !onlyNoOps(calls[1:]) {
		return nil, errAt(calls[1].pos, "%s does not return a cursor: .%s() is not supported", m.name, calls[1].name)
	}
	if c, ok := simpleCommands[m.name]; ok {
		if err := maxArgs(m, 0); err != nil {
			return nil, err
		}
		return x.reply(ctx, bson.D{{Key: c, Value: 1}})
	}
	switch m.name {
	case "runCommand", "adminCommand":
		return x.runCommand(ctx, m)
	case "createCollection", "createView":
		return x.create(ctx, m)
	case "getCollectionNames":
		if err := maxArgs(m, 0); err != nil {
			return nil, err
		}
		names, err := x.db.ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return nil, err
		}
		slices.Sort(names)
		vals := make([]any, len(names))
		for i, n := range names {
			vals[i] = n
		}
		return column("name", vals), nil
	case "getCollectionInfos":
		if err := maxArgs(m, 2); err != nil {
			return nil, err
		}
		filter, err := docArg(m, 0, "filter", true)
		if err != nil {
			return nil, err
		}
		opts, err := docArg(m, 1, "options", true)
		if err != nil {
			return nil, err
		}
		return x.cursor(ctx, false, append(bson.D{{Key: "listCollections", Value: 1}, {Key: "filter", Value: filter}}, opts...))
	case "dropDatabase":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		if _, err := x.command(ctx, false, bson.D{{Key: "dropDatabase", Value: 1}}); err != nil {
			return nil, err
		}
		return tagResult("dropped database " + x.db.Name()), nil
	case "stats":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		cmd := bson.D{{Key: "dbStats", Value: 1}}
		if v, ok := m.arg(0); ok {
			if d, isDoc := v.(bson.D); isDoc {
				cmd = append(cmd, d...)
			} else {
				cmd = append(cmd, bson.E{Key: "scale", Value: v})
			}
		}
		return x.reply(ctx, cmd)
	case "getName":
		if err := maxArgs(m, 0); err != nil {
			return nil, err
		}
		return column("name", []any{x.db.Name()}), nil
	case "version":
		if err := maxArgs(m, 0); err != nil {
			return nil, err
		}
		raw, err := x.command(ctx, false, bson.D{{Key: "buildInfo", Value: 1}})
		if err != nil {
			return nil, err
		}
		return column("version", []any{normalize(raw.Lookup("version"))}), nil
	case "currentOp":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		filter, err := docArg(m, 0, "filter", true)
		if err != nil {
			return nil, err
		}
		pipeline := bson.A{bson.D{{Key: "$currentOp", Value: bson.D{{Key: "allUsers", Value: true}}}}}
		if len(filter) > 0 {
			pipeline = append(pipeline, bson.D{{Key: "$match", Value: filter}})
		}
		admin := &executor{s: x.s, db: x.s.c.client.Database("admin")}
		return admin.cursor(ctx, false, bson.D{{Key: "aggregate", Value: 1}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: bson.D{}}})
	case "killOp":
		if err := maxArgs(m, 1); err != nil {
			return nil, err
		}
		op, ok := m.arg(0)
		if !ok {
			return nil, errAt(m.argAt(0), "killOp: missing operation id")
		}
		admin := &executor{s: x.s, db: x.s.c.client.Database("admin")}
		return admin.reply(ctx, bson.D{{Key: "killOp", Value: 1}, {Key: "op", Value: op}})
	}
	return nil, errAt(m.pos, "unknown or unsupported database method %s", m.name)
}

// runCommand runs a command document (or a command name) as is, outside
// any transaction. Commands that return a cursor stream their documents;
// the others return their reply.
func (x *executor) runCommand(ctx context.Context, m call) (driver.Result, error) {
	if err := maxArgs(m, 2); err != nil {
		return nil, err
	}
	var cmd bson.D
	switch v, _ := m.arg(0); v := v.(type) {
	case string:
		cmd = bson.D{{Key: v, Value: 1}}
	case bson.D:
		cmd = v
	default:
		return nil, errAt(m.argAt(0), "%s: expected a command document", m.name)
	}
	if len(cmd) == 0 {
		return nil, errAt(m.argAt(0), "%s: empty command", m.name)
	}
	if m.name == "adminCommand" {
		x = &executor{s: x.s, db: x.s.c.client.Database("admin")}
	}
	name := cmd[0].Key
	if explain, _ := get(cmd, "explain").(bool); cursorCommands[name] && !explain {
		if name == "aggregate" && !has(cmd, "cursor") {
			cmd = append(cmd, bson.E{Key: "cursor", Value: bson.D{}})
		}
		return x.cursor(ctx, false, cmd)
	}
	return x.reply(ctx, cmd)
}

func (x *executor) create(ctx context.Context, m call) (driver.Result, error) {
	view := m.name == "createView"
	nargs := 2
	if view {
		nargs = 4
	}
	if err := maxArgs(m, nargs); err != nil {
		return nil, err
	}
	name, err := stringArg(m, 0, "name")
	if err != nil {
		return nil, err
	}
	cmd := bson.D{{Key: "create", Value: name}}
	optArg := 1
	if view {
		source, err := stringArg(m, 1, "source collection")
		if err != nil {
			return nil, err
		}
		pipeline, _ := m.arg(2)
		if pipeline == nil {
			pipeline = bson.A{}
		}
		if _, ok := pipeline.(bson.A); !ok {
			return nil, errAt(m.argAt(2), "createView: the pipeline must be an array")
		}
		cmd = append(cmd, bson.E{Key: "viewOn", Value: source}, bson.E{Key: "pipeline", Value: pipeline})
		optArg = 3
	}
	opts, err := docArg(m, optArg, "options", true)
	if err != nil {
		return nil, err
	}
	if _, err := x.command(ctx, false, append(cmd, opts...)); err != nil {
		return nil, err
	}
	if view {
		return tagResult("created view " + name), nil
	}
	return tagResult("created collection " + name), nil
}

// show runs `show dbs` and `show collections`.
func (s *session) show(ctx context.Context, st *statement) (driver.Result, error) {
	ctx, err := s.opCtx(ctx, false)
	if err != nil {
		return nil, err
	}
	switch st.word {
	case "dbs", "databases":
		dbs, err := s.c.client.ListDatabases(ctx, bson.D{}, authorizedDatabases())
		if err != nil {
			return nil, err
		}
		rows := make([][]any, len(dbs.Databases))
		for i, d := range dbs.Databases {
			rows[i] = []any{d.Name, d.SizeOnDisk, d.Empty}
		}
		slices.SortFunc(rows, func(a, b []any) int { return strings.Compare(a[0].(string), b[0].(string)) })
		return table([]string{"name", "sizeOnDisk", "empty"}, rows), nil
	}
	colls, err := s.c.listCollections(ctx, s.db)
	if err != nil {
		return nil, err
	}
	rows := make([][]any, len(colls))
	for i, c := range colls {
		rows[i] = []any{c.Name, c.Type}
	}
	return table([]string{"name", "type"}, rows), nil
}

// ---------------------------------------------------------------------------
// Argument helpers

func maxArgs(c call, n int) error {
	if len(c.args) > n {
		return errAt(c.argAt(n), "%s: too many arguments (at most %d)", c.name, n)
	}
	return nil
}

// docArg returns argument i as a document; a missing (or null) optional
// argument is an empty document.
func docArg(c call, i int, what string, optional bool) (bson.D, error) {
	v, ok := c.arg(i)
	if !ok || v == nil {
		if optional {
			return bson.D{}, nil
		}
		return nil, errAt(c.argAt(i), "%s: missing %s", c.name, what)
	}
	d, ok := v.(bson.D)
	if !ok {
		return nil, errAt(c.argAt(i), "%s: the %s must be a document", c.name, what)
	}
	return d, nil
}

func intValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

func get(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

func has(d bson.D, key string) bool {
	return slices.ContainsFunc(d, func(e bson.E) bool { return e.Key == key })
}

// set replaces key's value, or appends it.
func set(d *bson.D, key string, v any) {
	for i := range *d {
		if (*d)[i].Key == key {
			(*d)[i].Value = v
			return
		}
	}
	*d = append(*d, bson.E{Key: key, Value: v})
}

func orEmpty(d bson.D) bson.D {
	if d == nil {
		return bson.D{}
	}
	return d
}
