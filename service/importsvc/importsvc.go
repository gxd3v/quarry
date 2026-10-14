// Package importsvc implements the import service: it previews CSV files and
// loads them into tables through the driver's Importer.
package importsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/importcsv"
	"github.com/gxd3v/quarry/service"
)

// progressEvery is the minimum interval between progress messages.
const progressEvery = 100 * time.Millisecond

type Service struct {
	live service.Live
}

var _ quarryv1connect.ImportServiceHandler = (*Service)(nil)

func New(live service.Live) *Service { return &Service{live: live} }

func (s *Service) PreviewCSV(ctx context.Context, req *connect.Request[quarryv1.PreviewCSVRequest]) (*connect.Response[quarryv1.PreviewCSVResponse], error) {
	m := req.Msg
	f, err := open(m.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var p importcsv.Preview
	if m.Format != nil {
		opts, err := optionsOf(m.Format)
		if err != nil {
			return nil, err
		}
		p, err = preview(f, opts)
		if err != nil {
			return nil, invalid("%v", err)
		}
	} else if p, err = importcsv.Sniff(f); err != nil {
		return nil, invalid("%v", err)
	}

	res := &quarryv1.PreviewCSVResponse{
		Format: &quarryv1.CSVFormat{Delimiter: string(p.Delimiter), HasHeader: p.HasHeader, Encoding: p.Encoding},
		Header: p.Header,
	}
	for _, r := range p.Rows {
		res.Rows = append(res.Rows, &quarryv1.CSVRow{Cells: r})
	}
	if m.Table != nil {
		cols, err := s.columns(ctx, m.ConnId, service.NodeOf(m.Table))
		if err != nil {
			return nil, err
		}
		res.TableColumns = cols
		res.Mapping = importcsv.AutoMap(p.Header, cols)
	}
	return connect.NewResponse(res), nil
}

// preview reads the header and the first rows with explicit options.
func preview(r io.Reader, o importcsv.Options) (importcsv.Preview, error) {
	p := importcsv.Preview{Delimiter: o.Delimiter, HasHeader: o.HasHeader, Encoding: o.Encoding}
	cr, err := importcsv.NewReader(r, importcsv.Options{Delimiter: o.Delimiter, Encoding: o.Encoding})
	if err != nil {
		return p, err
	}
	for len(p.Rows) < 20+1 {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return p, err
		}
		p.Rows = append(p.Rows, append([]string(nil), rec...))
	}
	if len(p.Rows) == 0 {
		return p, errors.New("the file is empty")
	}
	if o.HasHeader {
		p.Header, p.Rows = p.Rows[0], p.Rows[1:]
	} else {
		for i := range p.Rows[0] {
			p.Header = append(p.Header, fmt.Sprintf("column%d", i+1))
		}
	}
	p.Rows = p.Rows[:min(len(p.Rows), 20)]
	return p, nil
}

func (s *Service) columns(ctx context.Context, connID string, node *driver.Node) ([]string, error) {
	conn, _, err := s.live.Live(connID)
	if err != nil {
		return nil, service.LiveError(err)
	}
	d, ok := conn.(driver.Describer)
	if !ok {
		return nil, nil
	}
	det, err := d.Describe(ctx, node)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	cols := make([]string, len(det.Columns))
	for i, c := range det.Columns {
		cols[i] = c.Name
	}
	return cols, nil
}

// ImportCSV loads the file and streams progress; closing the stream cancels
// the import, which is then rolled back.
func (s *Service) ImportCSV(ctx context.Context, req *connect.Request[quarryv1.ImportCSVRequest], stream *connect.ServerStream[quarryv1.ImportCSVResponse]) error {
	return s.importCSV(ctx, req.Msg, stream.Send)
}

func (s *Service) importCSV(ctx context.Context, m *quarryv1.ImportCSVRequest, send func(*quarryv1.ImportCSVResponse) error) error {
	conn, cfg, err := s.live.Live(m.ConnId)
	if err != nil {
		return service.LiveError(err)
	}
	im, ok := conn.(driver.Importer)
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s connections cannot import files", cfg.Kind))
	}
	if cfg.ReadOnly {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("connection is read-only"))
	}
	if strings.EqualFold(cfg.Env, "prod") && !m.Confirmed {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("confirmation required: importing into a prod connection"))
	}
	if m.Table == nil {
		return invalid("no table to import into")
	}
	opts, err := optionsOf(m.Format)
	if err != nil {
		return err
	}
	var cols []string
	var idx []int
	for i, c := range m.Mapping {
		if c != "" {
			cols, idx = append(cols, c), append(idx, i)
		}
	}
	if len(cols) == 0 {
		return invalid("no file column is mapped to a table column")
	}
	f, err := open(m.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	cr, err := importcsv.NewReader(f, opts)
	if err != nil {
		return invalid("%v", err)
	}

	next := func() ([]any, error) {
		rec, err := cr.Read()
		if err != nil {
			if err != io.EOF {
				err = fmt.Errorf("line %d: %w", cr.Line(), err)
			}
			return nil, err
		}
		row := make([]any, len(idx))
		for j, i := range idx {
			switch {
			case i >= len(rec):
				return nil, fmt.Errorf("line %d has %d columns, column %d is mapped", cr.Line(), len(rec), i+1)
			case m.NullText != nil && rec[i] == *m.NullText, m.NullText == nil && rec[i] == "":
				row[j] = nil
			default:
				row[j] = rec[i]
			}
		}
		return row, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The first message flushes the response headers so the client sees the
	// import start.
	if err := send(&quarryv1.ImportCSVResponse{}); err != nil {
		return err
	}
	var (
		mu      sync.Mutex
		sendErr error
		last    = time.Now()
	)
	progress := func(rows int64) {
		mu.Lock()
		defer mu.Unlock()
		if sendErr != nil || time.Since(last) < progressEvery {
			return
		}
		last = time.Now()
		if sendErr = send(&quarryv1.ImportCSVResponse{Rows: rows}); sendErr != nil {
			cancel()
		}
	}
	rows, err := im.Import(ctx, service.NodeOf(m.Table), cols, next, progress)
	mu.Lock()
	defer mu.Unlock()
	if sendErr != nil {
		return sendErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return connect.NewError(connect.CodeCanceled, err)
		}
		if qe, ok := errors.AsType[*driver.QueryError](err); ok {
			return connect.NewError(connect.CodeInvalidArgument, qe)
		}
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return send(&quarryv1.ImportCSVResponse{Rows: rows, Done: true})
}

func optionsOf(f *quarryv1.CSVFormat) (importcsv.Options, error) {
	if f == nil {
		return importcsv.Options{}, invalid("no CSV format")
	}
	o := importcsv.Options{Delimiter: ',', HasHeader: f.HasHeader, Encoding: f.Encoding}
	if f.Delimiter != "" {
		r, n := utf8.DecodeRuneInString(f.Delimiter)
		if n != len(f.Delimiter) || r == '"' || r == '\r' || r == '\n' {
			return o, invalid("invalid delimiter %q", f.Delimiter)
		}
		o.Delimiter = r
	}
	switch o.Encoding {
	case "", importcsv.UTF8, importcsv.UTF8BOM:
		o.Encoding = ""
	case importcsv.UTF16LE, importcsv.UTF16BE, importcsv.Windows1252:
	default:
		return o, invalid("unknown encoding %q", f.Encoding)
	}
	return o, nil
}

func open(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, invalid("file path must be absolute: %q", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return f, nil
}

func invalid(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}
