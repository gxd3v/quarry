package importsvc

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/service"
)

type fakeLive struct {
	conn driver.Conn
	cfg  driver.Config
}

func (f *fakeLive) Live(string) (driver.Conn, driver.Config, error) {
	if f.conn == nil {
		return nil, f.cfg, service.ErrNotConnected
	}
	return f.conn, f.cfg, nil
}

func (f *fakeLive) Config(string) (driver.Config, bool) { return f.cfg, true }

type fakeConn struct {
	driver.Conn
	cols    []string
	got     [][]any
	columns []string
	fail    error
}

func (c *fakeConn) Describe(context.Context, *driver.Node) (*driver.ObjectDetails, error) {
	d := &driver.ObjectDetails{}
	for _, n := range c.cols {
		d.Columns = append(d.Columns, driver.ColumnInfo{Name: n})
	}
	return d, nil
}

func (c *fakeConn) Import(ctx context.Context, _ *driver.Node, columns []string, next func() ([]any, error), progress func(int64)) (int64, error) {
	c.columns = columns
	for {
		row, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		c.got = append(c.got, row)
		progress(int64(len(c.got)))
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return int64(len(c.got)), c.fail
}

func writeFile(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreviewCSV(t *testing.T) {
	c := &fakeConn{cols: []string{"id", "full_name", "email"}}
	s := New(&fakeLive{conn: c, cfg: driver.Config{Kind: driver.Postgres}})
	path := writeFile(t, "ID;Full Name;Phone\n1;ann;555\n2;bob;556\n")

	res, err := s.PreviewCSV(t.Context(), connect.NewRequest(&quarryv1.PreviewCSVRequest{Path: path, Table: &quarryv1.Node{Path: []string{"db", "public", "tables", "t"}}}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg
	if m.Format.Delimiter != ";" || !m.Format.HasHeader || len(m.Rows) != 2 || !slices.Equal(m.Mapping, []string{"id", "full_name", ""}) || len(m.TableColumns) != 3 {
		t.Fatalf("preview %v", m)
	}

	// An explicit format overrides the sniffed one.
	res, err = s.PreviewCSV(t.Context(), connect.NewRequest(&quarryv1.PreviewCSVRequest{Path: path, Format: &quarryv1.CSVFormat{Delimiter: ",", HasHeader: false}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Header) != 1 || res.Msg.Header[0] != "column1" || len(res.Msg.Rows) != 3 || res.Msg.Mapping != nil {
		t.Fatalf("explicit %v", res.Msg)
	}

	for _, p := range []string{"relative.csv", filepath.Join(t.TempDir(), "missing.csv")} {
		if _, err := s.PreviewCSV(t.Context(), connect.NewRequest(&quarryv1.PreviewCSVRequest{Path: p})); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}

func TestImportCSV(t *testing.T) {
	path := writeFile(t, "id,name,skip\n1,ann,x\n2,,y\n3,NULL,z\n")
	req := func() *quarryv1.ImportCSVRequest {
		return &quarryv1.ImportCSVRequest{
			Path: path, Table: &quarryv1.Node{Path: []string{"t"}},
			Format:  &quarryv1.CSVFormat{Delimiter: ",", HasHeader: true},
			Mapping: []string{"id", "name", ""},
		}
	}
	var sent []*quarryv1.ImportCSVResponse
	send := func(r *quarryv1.ImportCSVResponse) error { sent = append(sent, r); return nil }

	c := &fakeConn{}
	s := New(&fakeLive{conn: c, cfg: driver.Config{Kind: driver.Postgres}})
	if err := s.importCSV(t.Context(), req(), send); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.columns, []string{"id", "name"}) || len(c.got) != 3 || c.got[1][1] != nil || c.got[2][1] != "NULL" {
		t.Fatalf("imported %v into %v", c.got, c.columns)
	}
	if last := sent[len(sent)-1]; !last.Done || last.Rows != 3 {
		t.Fatalf("last message %v", last)
	}

	// With a NULL text, empty cells stay empty strings.
	c = &fakeConn{}
	s = New(&fakeLive{conn: c, cfg: driver.Config{Kind: driver.Postgres}})
	r := req()
	r.NullText = new("NULL")
	if err := s.importCSV(t.Context(), r, send); err != nil {
		t.Fatal(err)
	}
	if c.got[1][1] != "" || c.got[2][1] != nil {
		t.Fatalf("null text: %v", c.got)
	}

	for _, tc := range []struct {
		name string
		live *fakeLive
		mod  func(*quarryv1.ImportCSVRequest)
		code connect.Code
	}{
		{"not connected", &fakeLive{}, nil, connect.CodeFailedPrecondition},
		{"no importer", &fakeLive{conn: struct{ driver.Conn }{}}, nil, connect.CodeUnimplemented},
		{"read-only", &fakeLive{conn: &fakeConn{}, cfg: driver.Config{ReadOnly: true}}, nil, connect.CodeFailedPrecondition},
		{"prod", &fakeLive{conn: &fakeConn{}, cfg: driver.Config{Env: "prod"}}, nil, connect.CodeFailedPrecondition},
		{"nothing mapped", &fakeLive{conn: &fakeConn{}}, func(r *quarryv1.ImportCSVRequest) { r.Mapping = []string{"", ""} }, connect.CodeInvalidArgument},
		{"bad delimiter", &fakeLive{conn: &fakeConn{}}, func(r *quarryv1.ImportCSVRequest) { r.Format.Delimiter = "ab" }, connect.CodeInvalidArgument},
		{"mapped past the end", &fakeLive{conn: &fakeConn{}}, func(r *quarryv1.ImportCSVRequest) { r.Mapping = []string{"", "", "", "x"} }, connect.CodeInvalidArgument},
		{"driver error", &fakeLive{conn: &fakeConn{fail: &driver.QueryError{Msg: "bad"}}}, nil, connect.CodeInvalidArgument},
	} {
		r := req()
		if tc.mod != nil {
			tc.mod(r)
		}
		err := New(tc.live).importCSV(t.Context(), r, send)
		if ce, ok := errors.AsType[*connect.Error](err); !ok || ce.Code() != tc.code {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.code)
		}
	}

	// Prod with confirmation goes through.
	r = req()
	r.Confirmed = true
	if err := New(&fakeLive{conn: &fakeConn{}, cfg: driver.Config{Env: "prod"}}).importCSV(t.Context(), r, send); err != nil {
		t.Fatal(err)
	}

	// A failed send cancels the import.
	fails := 0
	err := New(&fakeLive{conn: &fakeConn{}}).importCSV(context.Background(), req(), func(r *quarryv1.ImportCSVResponse) error {
		if fails++; fails > 1 {
			return errors.New("client gone")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "client gone") {
		t.Fatalf("send failure: %v", err)
	}
}
