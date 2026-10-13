package service

import (
	"errors"
	"fmt"
	"testing"
	"unicode/utf16"

	"connectrpc.com/connect"
)

func TestOffsets(t *testing.T) {
	s := "aé😀b\n€"
	u := utf16.Encode([]rune(s))
	// Every UTF-16 boundary maps to the byte offset of the same character.
	for u16 := 0; u16 <= len(u); u16++ {
		b := ByteOffset(s, u16)
		if u16 == 3 { // inside the surrogate pair: rounds up to the next rune
			if b != 7 {
				t.Errorf("ByteOffset(%d) = %d, want 7", u16, b)
			}
			continue
		}
		want := len(string(utf16.Decode(u[:u16])))
		if b != want {
			t.Errorf("ByteOffset(%d) = %d, want %d", u16, b, want)
		}
		if back := UTF16Offset(s, b); back != u16 {
			t.Errorf("UTF16Offset(%d) = %d, want %d", b, back, u16)
		}
	}
	if ByteOffset(s, -1) != 0 || ByteOffset(s, 99) != len(s) || UTF16Offset(s, 99) != len(u) {
		t.Error("offsets are not clamped")
	}
}

func TestLiveError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code connect.Code
	}{
		{fmt.Errorf("x: %w", ErrNotConnected), connect.CodeFailedPrecondition},
		{connect.NewError(connect.CodeNotFound, errors.New("no such connection")), connect.CodeNotFound},
		{errors.New("boom"), connect.CodeUnavailable},
	} {
		if got := connect.CodeOf(LiveError(tc.err)); got != tc.code {
			t.Errorf("LiveError(%v) code = %v, want %v", tc.err, got, tc.code)
		}
	}
}
