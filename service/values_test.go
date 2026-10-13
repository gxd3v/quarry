package service

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/gxd3v/quarry/internal/driver"
)

func TestValueOf(t *testing.T) {
	ts := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, `{"null":true}`},
		{true, `{"bool":true}`},
		{int64(-7), `{"int":"-7"}`},
		{1.5, `{"float":1.5}`},
		{driver.Number("12.50"), `{"number":"12.50"}`},
		{"hi", `{"text":"hi"}`},
		{[]byte{1, 2}, `{"bytes":"AQI="}`},
		{ts, `{"time":"2026-10-13T12:00:00Z"}`},
		{driver.JSON(`{"a":1}`), `{"json":"{\"a\":1}"}`},
		{[]any{int64(1), nil}, `{"array":{"values":[{"int":"1"},{"null":true}]}}`},
		{uint8(3), `{"text":"3"}`},
	} {
		b, err := protojson.Marshal(ValueOf(tc.in))
		if err != nil {
			t.Fatal(err)
		}
		// protojson output is deliberately unstable in whitespace; compact it.
		var got []byte
		for _, c := range b {
			if c != ' ' {
				got = append(got, c)
			}
		}
		if string(got) != tc.want {
			t.Errorf("ValueOf(%#v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestValueFromRoundTrip(t *testing.T) {
	ts := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, in := range []any{nil, true, int64(-7), 1.5, driver.Number("12.50"), "hi", []byte{1, 2}, ts, driver.JSON(`{"a":1}`), []any{int64(1), nil}} {
		got := ValueFrom(ValueOf(in))
		if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", in) {
			t.Errorf("ValueFrom(ValueOf(%#v)) = %#v", in, got)
		}
	}
	if ValueFrom(nil) != nil {
		t.Error("ValueFrom(nil) != nil")
	}
}
