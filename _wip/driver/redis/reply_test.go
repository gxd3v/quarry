package redis

import (
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gxd3v/quarry/internal/driver"
)

func colNames(t table) []string {
	var out []string
	for _, c := range t.cols {
		out = append(out, c.Name)
	}
	return out
}

func checkTable(t *testing.T, name string, got table, cols []string, rows [][]any) {
	t.Helper()
	if !reflect.DeepEqual(colNames(got), cols) {
		t.Errorf("%s: columns = %q, want %q", name, colNames(got), cols)
	}
	if len(rows) == 0 && len(got.rows) == 0 {
		return
	}
	if !reflect.DeepEqual(got.rows, rows) {
		t.Errorf("%s: rows = %#v, want %#v", name, got.rows, rows)
	}
}

func args(s string) []string { return strings.Fields(s) }

func TestTabulateScalars(t *testing.T) {
	checkTable(t, "nil", tabulate(args("GET missing"), nil), []string{"value"}, [][]any{{nil}})
	checkTable(t, "status", tabulate(args("SET a b"), "OK"), []string{"value"}, [][]any{{"OK"}})
	checkTable(t, "string", tabulate(args("GET a"), "hello"), []string{"value"}, [][]any{{"hello"}})
	checkTable(t, "int", tabulate(args("INCR a"), int64(42)), []string{"value"}, [][]any{{int64(42)}})
	checkTable(t, "float", tabulate(args("ZSCORE z m"), 1.5), []string{"value"}, [][]any{{1.5}})
	checkTable(t, "bool", tabulate(args("HELLO"), true), []string{"value"}, [][]any{{true}})
	checkTable(t, "bigint", tabulate(args("X"), big.NewInt(7)), []string{"value"}, [][]any{{driver.Number("7")}})
	checkTable(t, "binary", tabulate(args("GET b"), "\xff\x00"), []string{"value"}, [][]any{{[]byte("\xff\x00")}})
	checkTable(t, "json", tabulate(args("JSON.GET k"), `{"a":1}`), []string{"value"}, [][]any{{driver.JSON(`{"a":1}`)}})
	if got := tabulate(args("INCR a"), int64(1)); got.cols[0].Type != "integer" {
		t.Errorf("int column type = %q", got.cols[0].Type)
	}
}

func TestTabulateArrays(t *testing.T) {
	checkTable(t, "array", tabulate(args("LRANGE l 0 -1"), []any{"a", nil, int64(3)}),
		[]string{"#", "value"}, [][]any{{int64(0), "a"}, {int64(1), nil}, {int64(2), int64(3)}})
	checkTable(t, "nested", tabulate(args("GEOPOS g a"), []any{[]any{"1.5", "2"}, nil}),
		[]string{"#", "value"}, [][]any{{int64(0), driver.JSON(`["1.5","2"]`)}, {int64(1), nil}})
	checkTable(t, "exec", tabulate(args("EXEC"), []any{"OK", goredis.Nil, errors.New("WRONGTYPE bad")}),
		[]string{"#", "value"}, [][]any{{int64(0), "OK"}, {int64(1), "(error) redis: nil"}, {int64(2), "(error) WRONGTYPE bad"}})
	checkTable(t, "empty", tabulate(args("SMEMBERS s"), []any{}), []string{"#", "value"}, nil)
}

func TestTabulatePairs(t *testing.T) {
	want := [][]any{{"a", "1"}, {"b", "2"}}
	checkTable(t, "hgetall resp2", tabulate(args("HGETALL h"), []any{"a", "1", "b", "2"}), []string{"field", "value"}, want)
	checkTable(t, "hgetall resp3", tabulate(args("hgetall h"), map[any]any{"b": "2", "a": "1"}), []string{"field", "value"}, want)
	checkTable(t, "config get", tabulate(args("CONFIG GET *max*"), []any{"a", "1", "b", "2"}), []string{"field", "value"}, want)
	checkTable(t, "hrandfield resp3", tabulate(args("HRANDFIELD h 2 WITHVALUES"), []any{[]any{"a", "1"}, []any{"b", "2"}}),
		[]string{"field", "value"}, want)
	checkTable(t, "generic map", tabulate(args("XINFO STREAM s"), map[any]any{"length": int64(2), "first-entry": []any{"1-0", []any{"f", "v"}}}),
		[]string{"field", "value"}, [][]any{{"first-entry", driver.JSON(`["1-0",["f","v"]]`)}, {"length", int64(2)}})
	// HRANDFIELD without WITHVALUES is a plain array.
	checkTable(t, "hrandfield", tabulate(args("HRANDFIELD h 2"), []any{"a", "b"}), []string{"#", "value"},
		[][]any{{int64(0), "a"}, {int64(1), "b"}})
}

func TestTabulateScores(t *testing.T) {
	want := [][]any{{"a", 1.0}, {"b", 2.5}}
	checkTable(t, "resp2", tabulate(args("ZRANGE z 0 -1 WITHSCORES"), []any{"a", "1", "b", "2.5"}), []string{"member", "score"}, want)
	checkTable(t, "resp3", tabulate(args("zrevrange z 0 -1 withscores"), []any{[]any{"a", 1.0}, []any{"b", 2.5}}), []string{"member", "score"}, want)
	checkTable(t, "zpopmin single", tabulate(args("ZPOPMIN z"), []any{"a", 1.0}), []string{"member", "score"}, [][]any{{"a", 1.0}})
	checkTable(t, "no scores", tabulate(args("ZRANGE z 0 -1"), []any{"a", "b"}), []string{"#", "value"},
		[][]any{{int64(0), "a"}, {int64(1), "b"}})
	got := tabulate(args("ZRANGE z 0 -1 WITHSCORES"), []any{"a", "inf"})
	if f, ok := got.rows[0][1].(float64); !ok || f < 1e308 {
		t.Errorf("inf score = %#v", got.rows[0][1])
	}
}

func TestTabulateScan(t *testing.T) {
	got := tabulate(args("SCAN 0 MATCH user:* COUNT 10"), []any{"17", []any{"user:1", "user:2"}})
	checkTable(t, "scan", got, []string{"key"}, [][]any{{"user:1"}, {"user:2"}})
	if len(got.msgs) != 1 || !strings.Contains(got.msgs[0].Text, "Next cursor: 17") ||
		!strings.Contains(got.msgs[0].Text, "SCAN 17 MATCH user:* COUNT 10") {
		t.Errorf("scan messages = %+v", got.msgs)
	}
	got = tabulate(args("SSCAN s 0"), []any{"0", []any{"m"}})
	checkTable(t, "sscan", got, []string{"member"}, [][]any{{"m"}})
	if len(got.msgs) != 1 || !strings.Contains(got.msgs[0].Text, "complete") {
		t.Errorf("sscan messages = %+v", got.msgs)
	}
	got = tabulate(args("HSCAN h 5 MATCH f*"), []any{"9", []any{"f1", "v1"}})
	checkTable(t, "hscan", got, []string{"field", "value"}, [][]any{{"f1", "v1"}})
	if !strings.Contains(got.msgs[0].Text, "HSCAN h 9 MATCH f*") {
		t.Errorf("hscan continuation = %q", got.msgs[0].Text)
	}
	checkTable(t, "hscan novalues", tabulate(args("HSCAN h 0 NOVALUES"), []any{"0", []any{"f1", "f2"}}),
		[]string{"field"}, [][]any{{"f1"}, {"f2"}})
	checkTable(t, "zscan", tabulate(args("ZSCAN z 0"), []any{"0", []any{"m", "3"}}),
		[]string{"member", "score"}, [][]any{{"m", 3.0}})
}

func TestTabulateStreams(t *testing.T) {
	entries := []any{
		[]any{"1-0", []any{"f1", "v1", "f2", "v2"}},
		[]any{"2-0", []any{"f1", "v3"}},
	}
	checkTable(t, "xrange", tabulate(args("XRANGE s - +"), entries), []string{"id", "field", "value"},
		[][]any{{"1-0", "f1", "v1"}, {"1-0", "f2", "v2"}, {"2-0", "f1", "v3"}})
	resp2 := []any{[]any{"s", entries[:1]}}
	checkTable(t, "xread resp2", tabulate(args("XREAD STREAMS s 0"), resp2), []string{"stream", "id", "field", "value"},
		[][]any{{"s", "1-0", "f1", "v1"}, {"s", "1-0", "f2", "v2"}})
	resp3 := map[any]any{"t": entries[1:], "s": entries[:1]}
	checkTable(t, "xread resp3", tabulate(args("XREAD COUNT 1 STREAMS s t 0 0"), resp3), []string{"stream", "id", "field", "value"},
		[][]any{{"s", "1-0", "f1", "v1"}, {"s", "1-0", "f2", "v2"}, {"t", "2-0", "f1", "v3"}})
	checkTable(t, "xclaim justid", tabulate(args("XCLAIM s g c 0 1-0 JUSTID"), []any{"1-0"}), []string{"#", "value"},
		[][]any{{int64(0), "1-0"}})
}

func TestTabulateInfo(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.4\r\nuptime_in_seconds:42\r\n\r\n# Keyspace\r\ndb0:keys=3,expires=0,avg_ttl=0\r\n"
	checkTable(t, "info", tabulate(args("INFO"), info), []string{"section", "key", "value"}, [][]any{
		{"Server", "redis_version", "7.2.4"},
		{"Server", "uptime_in_seconds", int64(42)},
		{"Keyspace", "db0", "keys=3,expires=0,avg_ttl=0"},
	})
	cl := "id=3 addr=127.0.0.1:5 name= db=0\nid=4 addr=127.0.0.1:6 name=x db=1\n"
	checkTable(t, "client list", tabulate(args("CLIENT LIST"), cl), []string{"id", "addr", "name", "db"}, [][]any{
		{int64(3), "127.0.0.1:5", "", int64(0)},
		{int64(4), "127.0.0.1:6", "x", int64(1)},
	})
}

func TestQueryError(t *testing.T) {
	qe, ok := queryError(goredis.ErrCrossSlot).(*driver.QueryError)
	if !ok || qe.Code != "CROSSSLOT" || qe.Msg != "Keys in request don't hash to the same slot" || qe.Position != -1 {
		t.Errorf("queryError = %#v", qe)
	}
	other := errors.New("dial tcp: refused")
	if queryError(other) != other {
		t.Error("non-server errors must pass through")
	}
	if queryError(nil) != nil {
		t.Error("nil")
	}
}
