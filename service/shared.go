package service

import (
	"errors"
	"unicode/utf8"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
)

// ErrNotConnected is returned by Live for a connection that is not open.
var ErrNotConnected = errors.New("connection is not open")

// Live resolves a connection id to its open connection. It is implemented by
// the connection manager and used by the explorer and query services.
type Live interface {
	// Live returns ErrNotConnected (possibly wrapped) when the connection is
	// saved but not open, and a connect NotFound error when the id is unknown.
	Live(id string) (driver.Conn, driver.Config, error)
	// Config returns a saved connection whether it is open or not.
	Config(id string) (driver.Config, bool)
}

// LiveError maps the error of Live.Live to a Connect error.
func LiveError(err error) error {
	if errors.Is(err, ErrNotConnected) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if ce := new(connect.Error); errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeUnavailable, err)
}

func ScopeOf(s *quarryv1.Scope) driver.Scope {
	if s == nil {
		return driver.Scope{}
	}
	return driver.Scope{Database: s.Database, Schema: s.Schema}
}

func ScopeProto(s driver.Scope) *quarryv1.Scope {
	return &quarryv1.Scope{Database: s.Database, Schema: s.Schema}
}

// NodeOf converts a protobuf node; nil stays nil (the explorer roots).
func NodeOf(n *quarryv1.Node) *driver.Node {
	if n == nil {
		return nil
	}
	return &driver.Node{Path: n.Path, Kind: driver.NodeKind(n.Kind), Name: n.Name, Detail: n.Detail, Leaf: n.Leaf}
}

func NodeProto(n *driver.Node) *quarryv1.Node {
	return &quarryv1.Node{Path: n.Path, Kind: string(n.Kind), Name: n.Name, Detail: n.Detail, Leaf: n.Leaf}
}

// ByteOffset converts a UTF-16 offset into s (as the editor counts) to a byte
// offset, clamped to [0, len(s)].
func ByteOffset(s string, u16 int) int {
	if u16 <= 0 {
		return 0
	}
	n := 0
	for i, r := range s {
		if n >= u16 {
			return i
		}
		n += utf16Len(r)
	}
	return len(s)
}

// UTF16Offset converts a byte offset into s to a UTF-16 offset. A byte offset
// inside a multi-byte character counts that character as consumed.
func UTF16Offset(s string, b int) int {
	b = min(max(b, 0), len(s))
	n := 0
	for i, r := range s {
		if i >= b {
			break
		}
		n += utf16Len(r)
	}
	return n
}

func utf16Len(r rune) int {
	if r >= 0x10000 && r <= utf8.MaxRune {
		return 2
	}
	return 1
}
