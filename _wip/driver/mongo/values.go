package mongo

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore"

	"github.com/gxd3v/quarry/internal/driver"
)

// docColumn is the result column holding each whole document.
const docColumn = "{document}"

// normalize converts a BSON value to one of the value types the UI knows
// (see driver.Result).
func normalize(rv bson.RawValue) any {
	switch rv.Type {
	case bson.TypeDouble:
		return rv.Double()
	case bson.TypeString:
		return rv.StringValue()
	case bson.TypeEmbeddedDocument, bson.TypeArray:
		return driver.JSON(extJSONValue(rv))
	case bson.TypeBinary:
		sub, data := rv.Binary()
		if sub == bson.TypeBinaryUUID && len(data) == 16 {
			return formatUUID(data)
		}
		return data
	case bson.TypeUndefined, bson.TypeNull:
		return nil
	case bson.TypeObjectID:
		return rv.ObjectID().Hex()
	case bson.TypeBoolean:
		return rv.Boolean()
	case bson.TypeDateTime:
		return time.UnixMilli(rv.DateTime()).UTC()
	case bson.TypeRegex:
		p, o := rv.Regex()
		return "/" + p + "/" + o
	case bson.TypeDBPointer:
		ns, id := rv.DBPointer()
		return fmt.Sprintf("DBPointer(%q, ObjectId(%q))", ns, id.Hex())
	case bson.TypeJavaScript:
		return rv.JavaScript()
	case bson.TypeSymbol:
		return rv.Symbol()
	case bson.TypeCodeWithScope:
		code, _ := rv.CodeWithScope()
		return code
	case bson.TypeInt32:
		return int64(rv.Int32())
	case bson.TypeTimestamp:
		t, i := rv.Timestamp()
		return fmt.Sprintf("Timestamp(%d, %d)", t, i)
	case bson.TypeInt64:
		return rv.Int64()
	case bson.TypeDecimal128:
		s := rv.Decimal128().String()
		switch s {
		case "NaN", "Infinity", "-Infinity":
			return s
		}
		return driver.Number(s)
	case bson.TypeMinKey:
		return "MinKey"
	case bson.TypeMaxKey:
		return "MaxKey"
	}
	return rv.String()
}

// normalizeGo normalizes a Go value (bson.D, ObjectID, int32, ...) by
// marshalling it first.
func normalizeGo(v any) any {
	rv, err := toRaw(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return normalize(rv)
}

func toRaw(v any) (bson.RawValue, error) {
	if v == nil {
		return bson.RawValue{Type: bson.TypeNull}, nil
	}
	if rv, ok := v.(bson.RawValue); ok {
		return rv, nil
	}
	t, b, err := bson.MarshalValue(v)
	if err != nil {
		return bson.RawValue{}, err
	}
	return bson.RawValue{Type: t, Value: b}, nil
}

func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// typeName returns the $type alias of a BSON type ("objectId", "int", ...);
// UUID binaries are "uuid".
func typeName(rv bson.RawValue) string {
	switch rv.Type {
	case bson.TypeDouble:
		return "double"
	case bson.TypeString:
		return "string"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		if sub, data := rv.Binary(); sub == bson.TypeBinaryUUID && len(data) == 16 {
			return "uuid"
		}
		return "binData"
	case bson.TypeUndefined:
		return "undefined"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeNull:
		return "null"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeDBPointer:
		return "dbPointer"
	case bson.TypeJavaScript:
		return "javascript"
	case bson.TypeSymbol:
		return "symbol"
	case bson.TypeCodeWithScope:
		return "javascriptWithScope"
	case bson.TypeInt32:
		return "int"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDecimal128:
		return "decimal"
	case bson.TypeMinKey:
		return "minKey"
	case bson.TypeMaxKey:
		return "maxKey"
	}
	return "unknown"
}

// extJSON renders a document as relaxed Extended JSON.
func extJSON(doc bson.Raw) string {
	b, err := bson.MarshalExtJSON(doc, false, false)
	if err != nil {
		return doc.String()
	}
	return string(b)
}

// extJSONValue renders any BSON value as relaxed Extended JSON.
func extJSONValue(rv bson.RawValue) string {
	if rv.Type == bson.TypeEmbeddedDocument {
		return extJSON(rv.Document())
	}
	idx, doc := bsoncore.AppendDocumentStart(nil)
	doc = bsoncore.AppendValueElement(doc, "v", bsoncore.Value{Type: bsoncore.Type(rv.Type), Data: rv.Value})
	doc, _ = bsoncore.AppendDocumentEnd(doc, idx)
	s := extJSON(bson.Raw(doc))
	// {"v":…}
	if strings.HasPrefix(s, `{"v":`) && strings.HasSuffix(s, "}") {
		return s[5 : len(s)-1]
	}
	return s
}

// docKeys returns the top-level keys of doc.
func docKeys(doc bson.Raw) []string {
	els, err := doc.Elements()
	if err != nil {
		return nil
	}
	keys := make([]string, len(els))
	for i, e := range els {
		keys[i] = e.Key()
	}
	return keys
}

// stripReply drops the cluster bookkeeping fields mongosh hides from
// command replies.
func stripReply(doc bson.Raw) bson.Raw {
	els, err := doc.Elements()
	if err != nil {
		return doc
	}
	drop := func(k string) bool {
		switch k {
		case "$clusterTime", "operationTime", "$configTime", "$topologyTime", "$gleStats", "lastCommittedOpTime", "electionId":
			return true
		}
		return false
	}
	need := false
	for _, e := range els {
		if drop(e.Key()) {
			need = true
			break
		}
	}
	if !need {
		return doc
	}
	idx, out := bsoncore.AppendDocumentStart(nil)
	for _, e := range els {
		if !drop(e.Key()) {
			out = append(out, e...)
		}
	}
	out, _ = bsoncore.AppendDocumentEnd(out, idx)
	return bson.Raw(out)
}

// marshalDoc turns a bson.D (or any document value) into raw BSON.
func marshalDoc(v any) (bson.Raw, error) {
	b, err := bson.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bson.Raw(b), nil
}

// ---------------------------------------------------------------------------
// mongosh syntax

// jsOpts controls formatJS output.
type jsOpts struct {
	indent string // "" renders on one line
	width  int    // documents shorter than this stay on one line when indenting
}

// formatJS renders a BSON value in mongosh syntax:
// { a: 1, _id: ObjectId("…"), at: ISODate("…") }.
func formatJS(v any) string {
	rv, err := toRaw(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	var b strings.Builder
	writeJS(&b, rv, jsOpts{}, "")
	return b.String()
}

// formatJSIndent is formatJS with nested documents broken over lines when
// they do not fit in 72 columns.
func formatJSIndent(v any) string {
	rv, err := toRaw(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	var b strings.Builder
	writeJS(&b, rv, jsOpts{indent: "  ", width: 72}, "")
	return b.String()
}

func writeJS(b *strings.Builder, rv bson.RawValue, o jsOpts, prefix string) {
	switch rv.Type {
	case bson.TypeEmbeddedDocument, bson.TypeArray:
		isDoc := rv.Type == bson.TypeEmbeddedDocument
		var els []bson.RawElement
		var vals []bson.RawValue
		if isDoc {
			els, _ = rv.Document().Elements()
		} else {
			vals, _ = rv.Array().Values()
		}
		n := len(els) + len(vals)
		open, close := "[", "]"
		if isDoc {
			open, close = "{", "}"
		}
		if n == 0 {
			b.WriteString(open + close)
			return
		}
		item := func(bb *strings.Builder, i int, oo jsOpts, pre string) {
			if isDoc {
				bb.WriteString(jsKey(els[i].Key()))
				bb.WriteString(": ")
				writeJS(bb, els[i].Value(), oo, pre)
			} else {
				writeJS(bb, vals[i], oo, pre)
			}
		}
		if o.indent != "" {
			var flat strings.Builder
			writeJS(&flat, rv, jsOpts{}, "")
			if len(prefix)+flat.Len() <= o.width {
				b.WriteString(flat.String())
				return
			}
			inner := prefix + o.indent
			b.WriteString(open + "\n")
			for i := range n {
				b.WriteString(inner)
				item(b, i, o, inner)
				if i < n-1 {
					b.WriteByte(',')
				}
				b.WriteByte('\n')
			}
			b.WriteString(prefix + close)
			return
		}
		if isDoc {
			b.WriteString("{ ")
		} else {
			b.WriteString("[")
		}
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			item(b, i, o, prefix)
		}
		if isDoc {
			b.WriteString(" }")
		} else {
			b.WriteString("]")
		}
	case bson.TypeDouble:
		f := rv.Double()
		switch {
		case math.IsNaN(f):
			b.WriteString("NaN")
		case math.IsInf(f, 1):
			b.WriteString("Infinity")
		case math.IsInf(f, -1):
			b.WriteString("-Infinity")
		case f == math.Trunc(f) && math.Abs(f) < 1e21:
			// Keep a decimal point so the value stays a double when parsed back.
			b.WriteString(strconv.FormatFloat(f, 'f', 1, 64))
		default:
			b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
		}
	case bson.TypeString:
		b.WriteString(jsQuote(rv.StringValue()))
	case bson.TypeBinary:
		sub, data := rv.Binary()
		if sub == bson.TypeBinaryUUID && len(data) == 16 {
			fmt.Fprintf(b, "UUID(%q)", formatUUID(data))
			return
		}
		fmt.Fprintf(b, "BinData(%d, %q)", sub, base64Std(data))
	case bson.TypeUndefined:
		b.WriteString("undefined")
	case bson.TypeObjectID:
		fmt.Fprintf(b, "ObjectId(%q)", rv.ObjectID().Hex())
	case bson.TypeBoolean:
		b.WriteString(strconv.FormatBool(rv.Boolean()))
	case bson.TypeDateTime:
		t := time.UnixMilli(rv.DateTime()).UTC()
		fmt.Fprintf(b, "ISODate(%q)", t.Format("2006-01-02T15:04:05.000Z07:00"))
	case bson.TypeNull:
		b.WriteString("null")
	case bson.TypeRegex:
		p, opts := rv.Regex()
		if p == "" || strings.ContainsAny(p, "\n\r") || unescapedSlash(p) {
			fmt.Fprintf(b, "RegExp(%s, %s)", jsQuote(p), jsQuote(opts))
			return
		}
		b.WriteString("/" + p + "/" + opts)
	case bson.TypeInt32:
		b.WriteString(strconv.FormatInt(int64(rv.Int32()), 10))
	case bson.TypeInt64:
		n := rv.Int64()
		if n >= math.MinInt32 && n <= math.MaxInt32 {
			fmt.Fprintf(b, "NumberLong(%d)", n)
		} else {
			fmt.Fprintf(b, "NumberLong(%q)", strconv.FormatInt(n, 10))
		}
	case bson.TypeDecimal128:
		fmt.Fprintf(b, "NumberDecimal(%q)", rv.Decimal128().String())
	case bson.TypeTimestamp:
		t, i := rv.Timestamp()
		fmt.Fprintf(b, "Timestamp({ t: %d, i: %d })", t, i)
	case bson.TypeMinKey:
		b.WriteString("MinKey()")
	case bson.TypeMaxKey:
		b.WriteString("MaxKey()")
	case bson.TypeJavaScript:
		b.WriteString(jsQuote(rv.JavaScript()))
	case bson.TypeSymbol:
		b.WriteString(jsQuote(rv.Symbol()))
	default:
		b.WriteString(jsQuote(rv.String()))
	}
}

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unescapedSlash(p string) bool {
	class := false
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if !class {
				return true
			}
		}
	}
	return false
}

// jsKey renders a field name, quoting it unless it is a plain identifier.
func jsKey(k string) string {
	if isPlainIdent(k) {
		return k
	}
	return jsQuote(k)
}

func isPlainIdent(s string) bool {
	if s == "" || isDigit(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c == '$' || isDigit(c) || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')) {
			return false
		}
	}
	return true
}

// jsQuote renders s as a double-quoted JS string.
func jsQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case ' ', ' ':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
