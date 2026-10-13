package redis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
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
	checkTable(t, "bool", tabulate(args("X"), true), []string{"value"}, [][]any{{true}})
	checkTable(t, "bigint", tabulate(args("X"), big.NewInt(7)), []string{"value"}, [][]any{{driver.Number("7")}})
	checkTable(t, "binary", tabulate(args("GET b"), "\xff\x00"), []string{"value"}, [][]any{{[]byte("\xff\x00")}})
	checkTable(t, "json", tabulate(args("JSON.GET k"), `{"a":1}`), []string{"value"}, [][]any{{driver.JSON(`{"a":1}`)}})
	if got := tabulate(args("INCR a"), int64(1)); got.cols[0].Type != "integer" {
		t.Errorf("int column type = %q", got.cols[0].Type)
	}
}

func TestTabulateArrays(t *testing.T) {
	checkTable(t, "array", tabulate(args("MGET a b c"), []any{"a", nil, int64(3)}),
		[]string{"#", "value"}, [][]any{{int64(0), "a"}, {int64(1), nil}, {int64(2), int64(3)}})
	checkTable(t, "lrange offset", tabulate(args("LRANGE l 5 6"), []any{"x", "y"}),
		[]string{"#", "value"}, [][]any{{int64(5), "x"}, {int64(6), "y"}})
	checkTable(t, "nested", tabulate(args("GEOPOS g a"), []any{[]any{"1.5", "2"}, nil}),
		[]string{"#", "value"}, [][]any{{int64(0), driver.JSON(`["1.5","2"]`)}, {int64(1), nil}})
	checkTable(t, "exec", tabulate(args("EXEC"), []any{"OK", errors.New("WRONGTYPE bad")}),
		[]string{"#", "value"}, [][]any{{int64(0), "OK"}, {int64(1), "(error) WRONGTYPE bad"}})
	checkTable(t, "smembers", tabulate(args("SMEMBERS s"), []any{"b", "a"}), []string{"member"}, [][]any{{"a"}, {"b"}})
	checkTable(t, "keys", tabulate(args("keys *"), []any{"k2", "k1"}), []string{"key"}, [][]any{{"k1"}, {"k2"}})
	checkTable(t, "empty", tabulate(args("SMEMBERS s"), []any{}), []string{"member"}, nil)
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
	if got.cursor != "17" {
		t.Errorf("cursor = %q", got.cursor)
	}
	got = tabulate(args("SSCAN s 0"), []any{"0", []any{"m"}})
	checkTable(t, "sscan", got, []string{"member"}, [][]any{{"m"}})
	if got.cursor != "0" {
		t.Errorf("sscan cursor = %q", got.cursor)
	}
	checkTable(t, "hscan", tabulate(args("HSCAN h 5 MATCH f*"), []any{"9", []any{"f1", "v1"}}),
		[]string{"field", "value"}, [][]any{{"f1", "v1"}})
	checkTable(t, "hscan novalues", tabulate(args("HSCAN h 0 NOVALUES"), []any{"0", []any{"f1", "f2"}}),
		[]string{"field"}, [][]any{{"f1"}, {"f2"}})
	checkTable(t, "zscan", tabulate(args("ZSCAN z 0"), []any{"0", []any{"m", "3"}}),
		[]string{"member", "score"}, [][]any{{"m", 3.0}})
	if got := tabulate(args("SCAN 0"), "QUEUED"); got.cursor != "" {
		t.Error("a queued SCAN has no cursor")
	}
}

func TestTabulateStreams(t *testing.T) {
	entries := []any{
		[]any{"1-0", []any{"f1", "v1", "f2", "v2"}},
		[]any{"2-0", []any{"f1", "v3", "id", "x"}},
	}
	checkTable(t, "xrange", tabulate(args("XRANGE s - +"), entries), []string{"id", "f1", "f2", "id (field)"},
		[][]any{{"1-0", "v1", "v2", nil}, {"2-0", "v3", nil, "x"}})
	resp2 := []any{[]any{"s", entries[:1]}}
	checkTable(t, "xread resp2", tabulate(args("XREAD STREAMS s 0"), resp2), []string{"stream", "id", "f1", "f2"},
		[][]any{{"s", "1-0", "v1", "v2"}})
	resp3 := map[any]any{"t": entries[1:], "s": entries[:1]}
	checkTable(t, "xread resp3", tabulate(args("XREAD COUNT 1 STREAMS s t 0 0"), resp3),
		[]string{"stream", "id", "f1", "f2", "id (field)"},
		[][]any{{"s", "1-0", "v1", "v2", nil}, {"t", "2-0", "v3", nil, "x"}})
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

type fakeRedisErr string

func (e fakeRedisErr) Error() string { return string(e) }
func (fakeRedisErr) RedisError()     {}

func TestQueryError(t *testing.T) {
	qe, ok := queryError(goredis.ErrCrossSlot).(*driver.QueryError)
	if !ok || qe.Code != "CROSSSLOT" || qe.Msg != "Keys in request don't hash to the same slot" || qe.Position != -1 {
		t.Errorf("queryError = %#v", qe)
	}
	qe, ok = queryError(fakeRedisErr("WRONGTYPE Operation against a key holding the wrong kind of value")).(*driver.QueryError)
	if !ok || qe.Code != "WRONGTYPE" || !strings.HasPrefix(qe.Msg, "Operation against") {
		t.Errorf("wrongtype = %#v", qe)
	}
	other := errors.New("dial tcp: refused")
	if queryError(other) != other {
		t.Error("non-server errors must pass through")
	}
	if queryError(goredis.Nil) != goredis.Nil || isServerError(goredis.TxFailedErr) {
		t.Error("go-redis sentinel errors are not server errors")
	}
	if queryError(nil) != nil {
		t.Error("nil")
	}
}

func TestConnectErrorAuth(t *testing.T) {
	for _, msg := range []string{
		"NOAUTH Authentication required.",
		"WRONGPASS invalid username-password pair or user is disabled.",
		"ERR invalid password",
		"ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?",
	} {
		err := connectError(fmt.Errorf("init: %w", fakeRedisErr(msg)), "h:1")
		if !errors.Is(err, driver.ErrAuth) {
			t.Errorf("%q does not wrap ErrAuth: %v", msg, err)
		}
		if !strings.HasPrefix(err.Error(), "authentication") {
			t.Errorf("%q message = %q", msg, err)
		}
	}
	for _, err := range []error{
		fakeRedisErr("ERR DB index is out of range"),
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		context.DeadlineExceeded,
		io.EOF,
	} {
		if ce := connectError(err, "h:1"); errors.Is(ce, driver.ErrAuth) {
			t.Errorf("%v wraps ErrAuth", err)
		}
	}
	if got := connectError(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, "h:1").Error(); got != "cannot connect to h:1: connection refused" {
		t.Errorf("dial error = %q", got)
	}
}
