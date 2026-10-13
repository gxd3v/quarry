package postgres

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestDecoders(t *testing.T) {
	tm := pgtype.NewMap()
	ts := time.Date(2024, 3, 1, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		oid  uint32
		in   string
		want any
	}{
		{pgtype.BoolOID, "t", true},
		{pgtype.BoolOID, "f", false},
		{pgtype.Int2OID, "-7", int64(-7)},
		{pgtype.Int4OID, "42", int64(42)},
		{pgtype.Int8OID, "9223372036854775807", int64(9223372036854775807)},
		{pgtype.OIDOID, "4294967295", int64(4294967295)},
		{pgtype.Float4OID, "1.5", 1.5},
		{pgtype.Float8OID, "-0.25", -0.25},
		{pgtype.NumericOID, "123.4500", driver.Number("123.4500")},
		{pgtype.NumericOID, "NaN", "NaN"},
		{pgtype.NumericOID, "-Infinity", "-Infinity"},
		{pgtype.JSONBOID, `{"a": [1, 2], "b": null}`, driver.JSON(`{"a":[1,2],"b":null}`)},
		{pgtype.JSONOID, `not json`, driver.JSON(`not json`)},
		{pgtype.ByteaOID, `\x0102ff`, []byte{1, 2, 255}},
		{pgtype.TextOID, "hello", "hello"},
		{pgtype.UUIDOID, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{pgtype.IntervalOID, "1 day 02:00:00", "1 day 02:00:00"},
		{pgtype.InetOID, "10.0.0.1/8", "10.0.0.1/8"},
		{pgtype.DateOID, "2024-03-01", time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)},
		{pgtype.DateOID, "infinity", "infinity"},
		{pgtype.TimestampOID, "2024-03-01 12:30:00", ts},
		{pgtype.TimestampOID, "-infinity", "-infinity"},
		{pgtype.Int4ArrayOID, "{1,NULL,3}", []any{int64(1), nil, int64(3)}},
		{pgtype.Int4ArrayOID, "{{1,2},{3,4}}", []any{[]any{int64(1), int64(2)}, []any{int64(3), int64(4)}}},
		{pgtype.Int4ArrayOID, "[0:1]={5,6}", []any{int64(5), int64(6)}},
		{pgtype.TextArrayOID, `{a,"b c","d,e","q\"x","NULL",NULL}`, []any{"a", "b c", "d,e", `q"x`, "NULL", nil}},
		{pgtype.TextArrayOID, "{}", []any{}},
		{pgtype.NumericArrayOID, "{1.5,NaN}", []any{driver.Number("1.5"), "NaN"}},
		{pgtype.JSONBArrayOID, `{"{\"a\": 1}"}`, []any{driver.JSON(`{"a":1}`)}},
		{pgtype.TextArrayOID, "{broken", "{broken"},
		{999999, "custom", "custom"},
	}
	for _, tt := range tests {
		got := newDecoder(tm, tt.oid, nil)([]byte(tt.in))
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("decode(oid %d, %q) = %#v, want %#v", tt.oid, tt.in, got, tt.want)
		}
	}

	tz := newDecoder(tm, pgtype.TimestamptzOID, nil)([]byte("2024-03-01 14:30:00+02"))
	if got, ok := tz.(time.Time); !ok || !got.Equal(ts) {
		t.Errorf("timestamptz = %#v, want %v", tz, ts)
	}

	box := newDecoder(tm, 1020, nil)([]byte("{(1,1),(0,0);(2,2),(1,1)}")) // box[]
	if want := []any{"(1,1),(0,0)", "(2,2),(1,1)"}; !reflect.DeepEqual(box, want) {
		t.Errorf("box[] = %#v, want %#v", box, want)
	}

	enumArr := map[uint32]typeInfo{70001: {name: "mood[]", array: true, elem: 70000, delim: ','}}
	if got := newDecoder(tm, 70001, enumArr)([]byte("{happy,sad}")); !reflect.DeepEqual(got, []any{"happy", "sad"}) {
		t.Errorf("enum array = %#v", got)
	}
}

func TestTypeName(t *testing.T) {
	tm := pgtype.NewMap()
	extra := map[uint32]typeInfo{70000: {name: "mood"}}
	for oid, want := range map[uint32]string{
		pgtype.Int4OID:      "int4",
		pgtype.Int4ArrayOID: "int4[]",
		pgtype.JSONBOID:     "jsonb",
		70000:               "mood",
		70001:               "70001",
	} {
		if got := typeName(tm, oid, extra); got != want {
			t.Errorf("typeName(%d) = %q, want %q", oid, got, want)
		}
	}
}

func TestBytePos(t *testing.T) {
	text := "select 'ção', x"
	tests := []struct{ pos, want int }{
		{0, -1},
		{1, 0},
		{8, 7},   // the opening quote
		{10, 10}, // 'ã' after the 2-byte 'ç'
		{15, 16}, // 'x'
		{99, len(text)},
	}
	for _, tt := range tests {
		if got := bytePos(text, tt.pos); got != tt.want {
			t.Errorf("bytePos(%d) = %d, want %d", tt.pos, got, tt.want)
		}
	}
}

func TestQueryError(t *testing.T) {
	err := queryError(&pgconn.PgError{Message: "boom", Detail: "d", Hint: "h", Code: "42601", Position: 3}, "ab€cd")
	qe, ok := err.(*driver.QueryError)
	if !ok {
		t.Fatalf("got %T", err)
	}
	want := driver.QueryError{Msg: "boom", Detail: "d", Hint: "h", Code: "42601", Position: 2}
	if *qe != want {
		t.Errorf("got %+v, want %+v", *qe, want)
	}
	if qe := queryError(&pgconn.PgError{Message: "x"}, "y").(*driver.QueryError); qe.Position != -1 {
		t.Errorf("no position: got %d", qe.Position)
	}
}

func TestRowEstimate(t *testing.T) {
	for in, want := range map[float64]string{
		-1:         "",
		0:          "~0",
		999:        "~999",
		1000:       "~1.0k",
		1250:       "~1.2k",
		12345:      "~12k",
		999999:     "~999k",
		1_500_000:  "~1.5M",
		42_000_000: "~42M",
		3e9:        "~3.0B",
	} {
		if got := rowEstimate(in); got != want {
			t.Errorf("rowEstimate(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDetails(t *testing.T) {
	if got := shortDefault("nextval('orders_id_seq'::regclass)"); got != "nextval('orders_id_seq')" {
		t.Errorf("shortDefault = %q", got)
	}
	if got := shortDefault("'draft'::character varying"); got != "'draft'" {
		t.Errorf("shortDefault = %q", got)
	}
	if got := shortDefault(strings.Repeat("x", 40)); len([]rune(got)) != 32 || !strings.HasSuffix(got, "…") {
		t.Errorf("shortDefault long = %q", got)
	}
	if got := columnDetail("integer", true, "nextval('s'::regclass)", "", ""); got != "integer not null = nextval('s')" {
		t.Errorf("columnDetail = %q", got)
	}
	if got := columnDetail("bigint", true, "", "a", ""); got != "bigint not null identity" {
		t.Errorf("columnDetail identity = %q", got)
	}
	if got := indexDetail("CREATE UNIQUE INDEX i ON public.t USING btree (customer_id)", false, true); got != "btree (customer_id) unique" {
		t.Errorf("indexDetail = %q", got)
	}
}

func TestNeedsTx(t *testing.T) {
	for stmt, want := range map[string]bool{
		"select 1":                         true,
		"insert into t values (1)":         true,
		"BEGIN":                            false,
		"commit":                           false,
		"vacuum t":                         false,
		"create database x":                false,
		"create index concurrently i on t": false,
		"create unique index concurrently": false,
		"create index i on t (a)":          true,
	} {
		if got := needsTx(keywords(stmt)); got != want {
			t.Errorf("needsTx(%q) = %v, want %v", stmt, got, want)
		}
	}
}

func TestAffected(t *testing.T) {
	for tag, want := range map[string]int64{
		"INSERT 0 3":   3,
		"UPDATE 2":     2,
		"DELETE 0":     0,
		"MERGE 4":      4,
		"SELECT 5":     5,
		"CREATE TABLE": -1,
		"SET":          -1,
	} {
		if got := affected(pgconn.NewCommandTag(tag)); got != want {
			t.Errorf("affected(%q) = %d, want %d", tag, got, want)
		}
	}
}

func TestBuildConfig(t *testing.T) {
	cfg := driver.Config{User: "bob", Options: map[string]string{
		"sslmode":          "disable",
		"application_name": "it's \\ me",
	}}
	pc, err := buildConfig(cfg, `p'w d\`)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Host != "localhost" || pc.Port != 5432 || pc.Database != "postgres" || pc.User != "bob" {
		t.Errorf("defaults: %s:%d/%s as %s", pc.Host, pc.Port, pc.Database, pc.User)
	}
	if pc.Password != `p'w d\` {
		t.Errorf("password = %q", pc.Password)
	}
	if got := pc.RuntimeParams["application_name"]; got != "it's \\ me" {
		t.Errorf("application_name = %q", got)
	}
	if pc.ConnectTimeout != 10*time.Second {
		t.Errorf("connect timeout = %v", pc.ConnectTimeout)
	}
	if pc.TLSConfig != nil {
		t.Error("sslmode=disable should disable TLS")
	}
	if pc.BuildContextWatcherHandler == nil {
		t.Error("context watcher handler not set")
	}

	inj := driver.Config{Database: "x' password='y", Options: map[string]string{"connect_timeout": "3"}}
	pc, err = buildConfig(inj, "")
	if err != nil {
		t.Fatal(err)
	}
	if pc.Database != "x' password='y" || pc.ConnectTimeout != 3*time.Second {
		t.Errorf("database = %q, timeout = %v", pc.Database, pc.ConnectTimeout)
	}

	if _, err := buildConfig(driver.Config{Options: map[string]string{"sslmode": "bogus"}}, "secret"); err == nil ||
		strings.Contains(err.Error(), "secret") {
		t.Errorf("bad sslmode: err = %v", err)
	}
}
