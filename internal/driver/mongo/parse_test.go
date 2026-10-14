package mongo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   []string
	}{
		{"db.a.find()", []string{"db.a.find()"}},
		{"db.a.find(); db.b.find()", []string{"db.a.find()", "db.b.find()"}},
		{"db.a.find()\ndb.b.find()", []string{"db.a.find()", "db.b.find()"}},
		{"db.a.find({\n  x: 1\n})\n  .sort({x: 1})\ndb.b.count()", []string{"db.a.find({\n  x: 1\n})\n  .sort({x: 1})", "db.b.count()"}},
		{"db.a.find().\n  limit(1)", []string{"db.a.find().\n  limit(1)"}},
		{"// only a comment\n\nuse x\nshow collections", []string{"// only a comment\n\nuse x", "show collections"}},
		{"db.a.insertOne({s: \"a;b\nc\"}) // tail", []string{"db.a.insertOne({s: \"a;b\nc\"}) // tail"}},
		{"  \n ", nil},
	} {
		var got []string
		for _, st := range Split(tc.script) {
			if tc.script[st.Offset:st.Offset+len(st.Text)] != st.Text {
				t.Errorf("%q: offset %d does not match %q", tc.script, st.Offset, st.Text)
			}
			got = append(got, st.Text)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Split(%q) = %q, want %q", tc.script, got, tc.want)
		}
	}
}

func TestStatementAt(t *testing.T) {
	script := "db.a.find()\n\ndb.b.find()"
	stmts := Split(script)
	for _, tc := range []struct {
		offset int
		want   string
	}{
		{0, "db.a.find()"},
		{11, "db.a.find()"},
		{12, "db.a.find()"},
		{13, "db.b.find()"},
		{len(script), "db.b.find()"},
	} {
		if st, ok := StatementAt(stmts, script, tc.offset); !ok || st.Text != tc.want {
			t.Errorf("offset %d: %q", tc.offset, st.Text)
		}
	}
}

func TestParseStatement(t *testing.T) {
	st, err := parseStatement(`db.getCollection("my coll").find({a: {$gt: 1}}, {_id: 0}).sort({a: -1}).limit(5)`)
	if err != nil {
		t.Fatal(err)
	}
	if !st.hasColl || st.coll != "my coll" || len(st.calls) != 3 || st.calls[0].name != "find" || st.calls[2].name != "limit" {
		t.Fatalf("parsed %+v", st)
	}
	st, err = parseStatement(`db.getSiblingDB("other").stats()`)
	if err != nil || !st.hasSibling || st.sibling != "other" || st.hasColl {
		t.Fatalf("sibling %+v %v", st, err)
	}
	st, err = parseStatement("use shop")
	if err != nil || st.kind != kUse || st.word != "shop" {
		t.Fatalf("use %+v %v", st, err)
	}

	for _, bad := range []string{"db.a.find({a: })", "db.a.find(", "select 1", `db.a.find({a: "x})`} {
		_, err := parseStatement(bad)
		pe, ok := err.(*parseError)
		if !ok || pe.pos < 0 || pe.pos > len(bad) {
			t.Errorf("%q: error %v", bad, err)
		}
	}
}

func TestParseValue(t *testing.T) {
	v, err := parseValue(`{n: 1, big: NumberLong("9007199254740993"), d: NumberDecimal("1.10"), f: 1.5,
		id: ObjectId("65a1b2c3d4e5f6a7b8c9d0e1"), at: ISODate("2024-01-02T03:04:05Z"), re: /ab+c/i,
		arr: [1, "two", null, true], 'quoted key': 'x', neg: -2}`)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := v.(bson.D)
	if !ok {
		t.Fatalf("%T", v)
	}
	m := map[string]any{}
	for _, e := range doc {
		m[e.Key] = e.Value
	}
	if m["n"] != int32(1) || m["big"] != int64(9007199254740993) || m["f"] != 1.5 || m["neg"] != int32(-2) || m["quoted key"] != "x" {
		t.Errorf("numbers and keys %v", m)
	}
	if id, ok := m["id"].(bson.ObjectID); !ok || id.Hex() != "65a1b2c3d4e5f6a7b8c9d0e1" {
		t.Errorf("object id %v", m["id"])
	}
	if at, ok := m["at"].(bson.DateTime); !ok || !at.Time().Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("date %v", m["at"])
	}
	if re, ok := m["re"].(bson.Regex); !ok || re.Pattern != "ab+c" || re.Options != "i" {
		t.Errorf("regex %v", m["re"])
	}
	if d, ok := m["d"].(bson.Decimal128); !ok || d.String() != "1.10" {
		t.Errorf("decimal %v", m["d"])
	}

	if _, err := parseValue("{a: 1} extra"); err == nil {
		t.Error("trailing input accepted")
	}
}

func TestFormatJS(t *testing.T) {
	id, _ := bson.ObjectIDFromHex("65a1b2c3d4e5f6a7b8c9d0e1")
	doc := bson.D{{Key: "_id", Value: id}, {Key: "name", Value: "ann"}, {Key: "n", Value: int64(5)}, {Key: "odd key", Value: true}}
	got := formatJS(doc)
	for _, want := range []string{`ObjectId("65a1b2c3d4e5f6a7b8c9d0e1")`, `name: "ann"`, `"odd key": true`} {
		if !strings.Contains(got, want) {
			t.Errorf("formatJS = %s, missing %s", got, want)
		}
	}
	// What formatJS prints parses back to the same document.
	back, err := parseValue(got)
	if err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if formatJS(back) != got {
		t.Errorf("round trip %s -> %s", got, formatJS(back))
	}
}
