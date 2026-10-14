package mongo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/gxd3v/quarry/internal/driver"
)

var (
	_ driver.DataViewer = (*conn)(nil)
	_ driver.Editable   = (*conn)(nil)
)

// collectionNode resolves a collection or view node ([db, name]).
func collectionNode(n *driver.Node) (db, coll string, err error) {
	if n == nil || len(n.Path) != 2 || (n.Kind != "" && n.Kind != driver.NodeCollection && n.Kind != driver.NodeView) {
		return "", "", errors.New("not a collection or view")
	}
	return n.Path[0], n.Path[1], nil
}

// DataQuery builds the statement behind a collection's data view:
//
//	db.getSiblingDB("app").getCollection("orders").find({ status: "open" }).sort({ total: -1 })
//
// It names the database, so it runs the same in any session. filter is one
// mongosh document literal ({ ... }, ObjectId(), ISODate(), regexes, ...);
// empty means every document. It is parsed and re-rendered, so it can only
// ever be a filter. Each orderBy term is a field name (dotted paths
// allowed), optionally followed by ASC or DESC.
func (c *conn) DataQuery(target *driver.Node, filter string, orderBy []string) (string, error) {
	db, coll, err := collectionNode(target)
	if err != nil {
		return "", err
	}
	f, err := dataFilter(filter)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "db.getSiblingDB(%s).getCollection(%s).find(%s)", jsQuote(db), jsQuote(coll), f)
	if len(orderBy) > 0 {
		sort := make(bson.D, 0, len(orderBy))
		for _, term := range orderBy {
			e, err := sortTerm(term)
			if err != nil {
				return "", err
			}
			sort = append(sort, e)
		}
		b.WriteString(".sort(" + formatJS(sort) + ")")
	}
	return b.String(), nil
}

func dataFilter(filter string) (string, error) {
	f := strings.TrimSpace(filter)
	for strings.HasSuffix(f, ";") {
		f = strings.TrimSpace(strings.TrimSuffix(f, ";"))
	}
	if f == "" {
		return "{}", nil
	}
	v, err := parseValue(f)
	if err != nil {
		return "", fmt.Errorf("invalid filter: %w", err)
	}
	if _, ok := v.(bson.D); !ok {
		return "", errors.New(`invalid filter: want a document like { status: "active" }`)
	}
	return formatJS(v), nil
}

func sortTerm(term string) (bson.E, error) {
	t := strings.TrimSpace(term)
	dir := int32(1)
	if i := strings.LastIndexAny(t, " \t"); i >= 0 {
		switch strings.ToUpper(t[i+1:]) {
		case "DESC":
			dir, t = -1, strings.TrimSpace(t[:i])
		case "ASC":
			t = strings.TrimSpace(t[:i])
		}
	}
	if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
		t = t[1 : len(t)-1]
	}
	if t == "" || strings.HasPrefix(t, "$") || strings.ContainsRune(t, 0) {
		return bson.E{}, fmt.Errorf("invalid sort term %q: want a field name optionally followed by ASC or DESC", term)
	}
	return bson.E{Key: t, Value: dir}, nil
}

// RowKey returns ["_id"] for regular collections, and nil for views, time
// series and system collections, whose documents cannot be edited.
func (c *conn) RowKey(ctx context.Context, target *driver.Node) ([]string, error) {
	db, coll, err := collectionNode(target)
	if err != nil {
		return nil, err
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	ci, err := c.collection(ctx, db, coll)
	if err != nil {
		return nil, err
	}
	if ci.Type != "collection" || strings.HasPrefix(coll, "system.") {
		return nil, nil
	}
	return []string{"_id"}, nil
}

// Apply saves edits to a collection. On a replica set or sharded cluster
// all changes run in one transaction on a dedicated server session (user
// sessions are never involved); a standalone server has no transactions,
// so changes are applied in order and the ones before a failure stay
// saved (the error says so).
//
// Keys and values are values as this driver returns them (see normalize),
// or as typed by the user:
//
//   - Key holds "_id". A string _id also matches an ObjectId or UUID with
//     that text, so ids shown as hex strings find their document; the
//     document must exist and be unique, or nothing is saved. Other fields
//     are not checked: the last writer wins for them.
//   - OpUpdate sets the changed top-level fields with $set; nil removes the
//     field ($unset) and driver.JSON("null") stores an explicit null. A new
//     value keeps the stored field's BSON type when it is a scalar: a string
//     written to an objectId field becomes an ObjectId, an int64 written to
//     an int field stays an int, and so on (an impossible conversion is an
//     error). driver.JSON values are relaxed Extended JSON and replace the
//     field as they are, which also changes a field's type explicitly.
//     DocumentColumn alone replaces the whole document (its _id must not
//     change).
//   - OpInsert inserts Values, nil values omitted, DocumentColumn (if any)
//     as the base document; without _id a new ObjectId is generated.
//   - OpDelete deletes the document.
func (c *conn) Apply(ctx context.Context, target *driver.Node, changes []driver.RowChange) error {
	if c.readOnly {
		return errReadOnly
	}
	if err := c.check(); err != nil {
		return err
	}
	db, coll, err := collectionNode(target)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	ci, err := c.collection(ctx, db, coll)
	if err != nil {
		return err
	}
	if ci.Type != "collection" || strings.HasPrefix(coll, "system.") {
		return fmt.Errorf("%s is not a regular collection: its documents cannot be edited", coll)
	}
	e := &editor{coll: c.client.Database(db).Collection(coll)}
	if !c.replicaSet {
		for i, ch := range changes {
			if err := e.apply(ctx, i, ch); err != nil {
				if i > 0 {
					return fmt.Errorf("%w (the %d changes before it were saved: a standalone server has no transactions)", err, i)
				}
				return err
			}
		}
		return nil
	}
	ms, err := c.client.StartSession()
	if err != nil {
		return queryError(err)
	}
	defer ms.EndSession(context.WithoutCancel(ctx))
	if err := ms.StartTransaction(); err != nil {
		return queryError(err)
	}
	sctx := mongo.NewSessionContext(ctx, ms)
	for i, ch := range changes {
		if err := e.apply(sctx, i, ch); err != nil {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
			ms.AbortTransaction(actx)
			cancel()
			return err
		}
	}
	if err := ms.CommitTransaction(ctx); err != nil {
		return changeError(err, "saving changes")
	}
	return nil
}

type editor struct {
	coll *mongo.Collection
}

func (e *editor) apply(ctx context.Context, i int, ch driver.RowChange) error {
	switch ch.Op {
	case driver.OpInsert:
		doc, err := insertDoc(ch.Values)
		if err != nil {
			return fmt.Errorf("change %d: %w", i+1, err)
		}
		doc, _ = withID(doc)
		if _, err := e.coll.InsertOne(ctx, doc); err != nil {
			return changeError(err, fmt.Sprintf("inserting document %d", i+1))
		}
		return nil
	case driver.OpUpdate, driver.OpDelete:
	default:
		return fmt.Errorf("change %d: unknown operation %d", i+1, ch.Op)
	}
	orig, desc, err := e.lookup(ctx, ch.Key)
	if err != nil {
		return fmt.Errorf("change %d: %w", i+1, err)
	}
	id := orig.Lookup("_id")
	byID := bson.D{{Key: "_id", Value: id}}
	if ch.Op == driver.OpDelete {
		res, err := e.coll.DeleteOne(ctx, byID)
		if err != nil {
			return changeError(err, "deleting document "+desc)
		}
		if res.DeletedCount != 1 {
			return fmt.Errorf("document %s was deleted by someone else; no changes were saved", desc)
		}
		return nil
	}
	var res *mongo.UpdateResult
	if _, whole := ch.Values[DocumentColumn]; whole {
		doc, err := replacement(id, ch.Values)
		if err != nil {
			return fmt.Errorf("change %d: %w", i+1, err)
		}
		res, err = e.coll.ReplaceOne(ctx, byID, doc)
		if err != nil {
			return changeError(err, "replacing document "+desc)
		}
	} else {
		upd, err := updateDoc(orig, ch.Values)
		if err != nil {
			return fmt.Errorf("change %d: %w", i+1, err)
		}
		if len(upd) == 0 {
			return nil
		}
		res, err = e.coll.UpdateOne(ctx, byID, upd)
		if err != nil {
			return changeError(err, "updating document "+desc)
		}
	}
	if res.MatchedCount != 1 {
		return fmt.Errorf("document %s was deleted by someone else; no changes were saved", desc)
	}
	return nil
}

// lookup finds the one document whose _id matches key.
func (e *editor) lookup(ctx context.Context, key map[string]any) (bson.Raw, string, error) {
	v, ok := key["_id"]
	if !ok {
		return nil, "", errors.New("the change has no _id")
	}
	desc := "_id=" + keyText(v)
	cands, err := idCandidates(v)
	if err != nil {
		return nil, desc, fmt.Errorf("invalid _id: %w", err)
	}
	cur, err := e.coll.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: cands}}}}, options.Find().SetLimit(2))
	if err != nil {
		return nil, desc, changeError(err, "looking up document "+desc)
	}
	var docs []bson.Raw
	for cur.Next(ctx) {
		docs = append(docs, slices.Clone(cur.Current))
	}
	cur.Close(context.WithoutCancel(ctx))
	if err := cur.Err(); err != nil {
		return nil, desc, changeError(err, "looking up document "+desc)
	}
	switch len(docs) {
	case 0:
		return nil, desc, fmt.Errorf("document %s changed or was deleted by someone else; no changes were saved", desc)
	case 1:
		return docs[0], desc, nil
	}
	return nil, desc, fmt.Errorf("document %s is ambiguous (an ObjectId and a string share that text); no changes were saved", desc)
}

func keyText(v any) string {
	switch v := v.(type) {
	case string:
		return strconv.Quote(v)
	case driver.JSON:
		return string(v)
	case time.Time:
		return v.Format(time.RFC3339Nano)
	}
	return fmt.Sprint(v)
}

// idCandidates returns the BSON values an _id key may stand for.
func idCandidates(v any) (bson.A, error) {
	switch v := v.(type) {
	case string:
		out := bson.A{v}
		if oid, err := bson.ObjectIDFromHex(v); err == nil {
			out = append(out, oid)
		}
		if len(v) == 36 {
			if b, err := parseUUID(v); err == nil {
				out = append(out, bson.Binary{Subtype: bson.TypeBinaryUUID, Data: b})
			}
		}
		return out, nil
	case []byte:
		out := bson.A{bson.Binary{Data: v}}
		if len(v) == 16 {
			out = append(out, bson.Binary{Subtype: bson.TypeBinaryUUID, Data: v})
		}
		return out, nil
	}
	b, err := toBSON(v, bson.RawValue{})
	if err != nil {
		return nil, err
	}
	return bson.A{b}, nil
}

// insertDoc builds a document to insert: DocumentColumn as the base, then
// the other values (nil omitted) in name order, _id first.
func insertDoc(values map[string]any) (bson.D, error) {
	doc := bson.D{}
	if v := values[DocumentColumn]; v != nil {
		base, err := documentValue(v)
		if err != nil {
			return nil, err
		}
		doc = base
	}
	for _, k := range fieldNames(values) {
		v := values[k]
		if v == nil {
			continue
		}
		bv, err := toBSON(v, bson.RawValue{})
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		set(&doc, k, bv)
	}
	return doc, nil
}

// fieldNames returns the field names of values, _id first, without
// DocumentColumn.
func fieldNames(values map[string]any) []string {
	var keys []string
	for k := range values {
		if k != DocumentColumn {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		switch {
		case a == "_id":
			return -1
		case b == "_id":
			return 1
		}
		return strings.Compare(a, b)
	})
	return keys
}

// updateDoc builds the $set/$unset update for changed top-level fields.
func updateDoc(orig bson.Raw, values map[string]any) (bson.D, error) {
	var setF, unset bson.D
	for _, k := range fieldNames(values) {
		v := values[k]
		if k == "_id" {
			if !sameID(orig.Lookup("_id"), v) {
				return nil, errors.New("_id cannot be changed")
			}
			continue
		}
		if k == "" || strings.ContainsRune(k, '.') || strings.HasPrefix(k, "$") {
			return nil, fmt.Errorf("field %q cannot be edited by name; edit the whole document instead", k)
		}
		if v == nil {
			unset = append(unset, bson.E{Key: k, Value: ""})
			continue
		}
		like, _ := orig.LookupErr(k)
		bv, err := toBSON(v, like)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		setF = append(setF, bson.E{Key: k, Value: bv})
	}
	var upd bson.D
	if len(setF) > 0 {
		upd = append(upd, bson.E{Key: "$set", Value: setF})
	}
	if len(unset) > 0 {
		upd = append(upd, bson.E{Key: "$unset", Value: unset})
	}
	return upd, nil
}

func sameID(id bson.RawValue, v any) bool {
	cands, err := idCandidates(v)
	if err != nil {
		return false
	}
	for _, c := range cands {
		if rv, err := toRaw(c); err == nil && rv.Equal(id) {
			return true
		}
	}
	return false
}

// replacement builds a whole-document replacement from DocumentColumn.
func replacement(id bson.RawValue, values map[string]any) (bson.D, error) {
	if len(fieldNames(values)) > 0 {
		return nil, errors.New("edit either the whole document or its fields, not both")
	}
	doc, err := documentValue(values[DocumentColumn])
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(doc, func(e bson.E) bool { return e.Key == "_id" }); i >= 0 {
		rv, err := toRaw(doc[i].Value)
		if err != nil || !rv.Equal(id) {
			return nil, errors.New("_id cannot be changed")
		}
		return doc, nil
	}
	return append(bson.D{{Key: "_id", Value: id}}, doc...), nil
}

// documentValue parses a whole document given as relaxed Extended JSON.
func documentValue(v any) (bson.D, error) {
	var text string
	switch v := v.(type) {
	case driver.JSON:
		text = string(v)
	case string:
		text = v
	default:
		return nil, errors.New("the document must be JSON text")
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &doc); err != nil {
		return nil, fmt.Errorf("invalid document JSON: %v", err)
	}
	return doc, nil
}

// parseJSONValue parses any relaxed Extended JSON value.
func parseJSONValue(text string) (any, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(`{"v":`+text+"\n}"), false, &d); err != nil || len(d) != 1 {
		return nil, fmt.Errorf("invalid JSON value: %s", strings.TrimSpace(text))
	}
	return d[0].Value, nil
}

// toBSON converts an edited value to BSON. like is the value it replaces
// (zero when there is none); scalars keep like's type when possible.
func toBSON(v any, like bson.RawValue) (any, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case driver.JSON:
		return parseJSONValue(string(v))
	case bool:
		return v, nil
	case int64:
		return intLike(v, like.Type), nil
	case float64:
		return floatLike(v, like.Type), nil
	case driver.Number:
		return numberLike(string(v), like.Type)
	case string:
		return stringLike(v, like)
	case time.Time:
		return bson.NewDateTimeFromTime(v), nil
	case []byte:
		sub := byte(0)
		if like.Type == bson.TypeBinary {
			sub, _ = like.Binary()
		}
		return bson.Binary{Subtype: sub, Data: v}, nil
	case []any:
		a := make(bson.A, len(v))
		for i, e := range v {
			b, err := toBSON(e, bson.RawValue{})
			if err != nil {
				return nil, err
			}
			a[i] = b
		}
		return a, nil
	}
	return nil, fmt.Errorf("unsupported value of type %T", v)
}

func intLike(n int64, t bson.Type) any {
	switch t {
	case bson.TypeDouble:
		return float64(n)
	case bson.TypeInt64:
		return n
	case bson.TypeDecimal128:
		d, _ := bson.ParseDecimal128(strconv.FormatInt(n, 10))
		return d
	}
	if n >= math.MinInt32 && n <= math.MaxInt32 {
		return int32(n)
	}
	return n
}

func floatLike(f float64, t bson.Type) any {
	switch t {
	case bson.TypeInt32, bson.TypeInt64:
		if f == math.Trunc(f) && math.Abs(f) < 1<<63 {
			return intLike(int64(f), t)
		}
	case bson.TypeDecimal128:
		if d, err := bson.ParseDecimal128(strconv.FormatFloat(f, 'g', -1, 64)); err == nil {
			return d
		}
	}
	return f
}

func numberLike(s string, t bson.Type) (any, error) {
	s = strings.TrimSpace(s)
	switch t {
	case bson.TypeInt32, bson.TypeInt64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", s)
		}
		return intLike(n, t), nil
	case bson.TypeDouble:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", s)
		}
		return f, nil
	}
	d, err := bson.ParseDecimal128(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not a decimal number", s)
	}
	return d, nil
}

var regexText = regexp.MustCompile(`^/(.*)/([imsxlu]*)$`)

// stringLike converts text typed into a field of like's type.
func stringLike(s string, like bson.RawValue) (any, error) {
	switch like.Type {
	case bson.TypeObjectID:
		oid, err := bson.ObjectIDFromHex(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%q is not an ObjectId (24 hex digits)", s)
		}
		return oid, nil
	case bson.TypeBinary:
		if sub, _ := like.Binary(); sub == bson.TypeBinaryUUID {
			b, err := parseUUID(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("%q is not a UUID", s)
			}
			return bson.Binary{Subtype: sub, Data: b}, nil
		}
	case bson.TypeInt32, bson.TypeInt64, bson.TypeDouble, bson.TypeDecimal128:
		return numberLike(s, like.Type)
	case bson.TypeDateTime:
		t, err := parseDate(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a date", s)
		}
		return bson.NewDateTimeFromTime(t), nil
	case bson.TypeBoolean:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", s)
		}
		return b, nil
	case bson.TypeEmbeddedDocument, bson.TypeArray:
		if v, err := parseJSONValue(s); err == nil {
			return v, nil
		}
	case bson.TypeRegex:
		if m := regexText.FindStringSubmatch(s); m != nil {
			return bson.Regex{Pattern: m[1], Options: m[2]}, nil
		}
	}
	return s, nil
}

// changeError names the failed change in a server error.
func changeError(err error, label string) error {
	qe, ok := queryError(err).(*driver.QueryError)
	if !ok {
		return fmt.Errorf("%s: %w", label, err)
	}
	q := *qe
	q.Msg = label + ": " + q.Msg
	q.Position = -1
	return &q
}
