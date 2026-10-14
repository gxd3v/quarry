// Package export implements the export service: it streams the full result
// of one read-only statement to a file, on a session of its own.
package export

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	"github.com/gxd3v/quarry/internal/driver/oracle"
	"github.com/gxd3v/quarry/internal/driver/postgres"
	"github.com/gxd3v/quarry/internal/export"
	"github.com/gxd3v/quarry/internal/sqlsafe"
	"github.com/gxd3v/quarry/service"
)

// progressEvery is the minimum interval between progress messages.
const progressEvery = 100 * time.Millisecond

type Service struct {
	live service.Live
}

var _ quarryv1connect.ExportServiceHandler = (*Service)(nil)

func New(live service.Live) *Service { return &Service{live: live} }

// Export writes the file and streams progress; the client closing the stream
// cancels it and the partial file is removed.
func (s *Service) Export(ctx context.Context, req *connect.Request[quarryv1.ExportRequest], stream *connect.ServerStream[quarryv1.ExportResponse]) error {
	return s.export(ctx, req.Msg, stream.Send)
}

func (s *Service) export(ctx context.Context, req *quarryv1.ExportRequest, send func(*quarryv1.ExportResponse) error) error {
	if !filepath.IsAbs(req.Path) {
		return invalid("export path must be absolute: %q", req.Path)
	}
	if err := checkDir(filepath.Dir(req.Path)); err != nil {
		return err
	}
	conn, cfg, err := s.live.Live(req.ConnId)
	if err != nil {
		return service.LiveError(err)
	}
	stmt, table, err := source(conn, cfg.Kind, req)
	if err != nil {
		return err
	}
	opts, err := options(req.Options, req.Path, cfg.Kind, table)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess, err := conn.NewSession(ctx, service.ScopeOf(req.Scope))
	if err != nil {
		return runError(ctx, err)
	}
	defer sess.Close()
	res, err := sess.Execute(ctx, stmt)
	if err != nil {
		return runError(ctx, err)
	}
	defer res.Close()
	cols := res.Columns()
	if len(cols) == 0 {
		return invalid("statement returns no rows")
	}
	// The first message flushes the response headers, so the client sees
	// the export start before the first page arrives.
	if err := send(&quarryv1.ExportResponse{}); err != nil {
		return err
	}

	var sendErr error
	last := time.Now()
	progress := func(rows int64) {
		if sendErr != nil || time.Since(last) < progressEvery {
			return
		}
		last = time.Now()
		if sendErr = send(&quarryv1.ExportResponse{Rows: rows}); sendErr != nil {
			cancel()
		}
	}
	rows, err := export.WriteFile(ctx, req.Path, cols, res.Next, opts, progress)
	if sendErr != nil {
		return sendErr
	}
	if err != nil {
		return runError(ctx, err)
	}
	return send(&quarryv1.ExportResponse{Rows: rows, Done: true})
}

// source resolves the request to the single statement to run and, for a data
// source, the quoted table name SQL INSERT exports default to.
func source(conn driver.Conn, kind driver.Kind, req *quarryv1.ExportRequest) (driver.Statement, string, error) {
	var text, table string
	switch src := req.Source.(type) {
	case *quarryv1.ExportRequest_Sql:
		text = src.Sql
	case *quarryv1.ExportRequest_Data:
		dv, ok := conn.(driver.DataViewer)
		if !ok {
			return driver.Statement{}, "", connect.NewError(connect.CodeUnimplemented, errors.New("this connection has no data view to export"))
		}
		node := service.NodeOf(src.Data.GetNode())
		q, err := dv.DataQuery(node, src.Data.Filter, src.Data.OrderBy)
		if err != nil {
			return driver.Statement{}, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		text, table = q, tableName(node, kind)
	default:
		return driver.Statement{}, "", invalid("export needs a statement or a data source")
	}
	stmts := split(kind, text)
	switch {
	case len(stmts) == 0:
		return driver.Statement{}, "", invalid("no statement to export")
	case len(stmts) > 1:
		return driver.Statement{}, "", invalid("export runs exactly one statement, got %d", len(stmts))
	}
	if sqlsafe.IsWrite(language(kind), stmts[0].Text) {
		return driver.Statement{}, "", invalid("export only runs statements that do not modify data")
	}
	return stmts[0], table, nil
}

func split(kind driver.Kind, script string) []driver.Statement {
	switch kind {
	case driver.Postgres:
		return postgres.Split(script)
	case driver.Oracle:
		return oracle.Split(script)
	}
	text := strings.TrimSpace(script)
	if text == "" {
		return nil
	}
	return []driver.Statement{{Text: text, Offset: strings.Index(script, text)}}
}

func language(kind driver.Kind) driver.Language {
	switch kind {
	case driver.MongoDB:
		return driver.LangMongo
	case driver.Redis:
		return driver.LangRedis
	}
	return driver.LangSQL
}

// tableName quotes the schema-qualified name of a table node, whose path ends
// in [schema, folder, name].
func tableName(n *driver.Node, kind driver.Kind) string {
	if n == nil || len(n.Path) == 0 {
		return ""
	}
	p := n.Path
	name := export.QuoteIdent(p[len(p)-1], string(kind))
	if len(p) < 3 {
		return name
	}
	return export.QuoteIdent(p[len(p)-3], string(kind)) + "." + name
}

var formats = map[string]export.Format{
	"csv":      export.CSV,
	"tsv":      export.TSV,
	"json":     export.JSON,
	"ndjson":   export.NDJSON,
	"sql":      export.SQLInsert,
	"markdown": export.Markdown,
}

// options maps the request options onto the defaults of their format. An
// empty format is taken from the path's extension, falling back to CSV;
// without options at all the format's defaults apply.
func options(o *quarryv1.ExportOptions, path string, kind driver.Kind, table string) (export.Options, error) {
	f, ok := formats[strings.ToLower(o.GetFormat())]
	if o.GetFormat() == "" {
		f, _ = export.FormatFromExt(path)
	} else if !ok {
		return export.Options{}, invalid("unknown export format %q", o.GetFormat())
	}
	opts := export.DefaultOptions(f)
	opts.Dialect = string(kind)
	if table != "" {
		opts.Table = table
	}
	if o == nil {
		return opts, nil
	}
	if d := o.Delimiter; d != "" {
		r, size := utf8.DecodeRuneInString(d)
		if size != len(d) || r == utf8.RuneError || strings.ContainsRune("\"\r\n", r) {
			return export.Options{}, invalid("delimiter must be a single character other than a quote or line break, got %q", d)
		}
		opts.Delimiter = r
	}
	opts.Header, opts.NullText, opts.QuoteAll, opts.CRLF, opts.BOM = o.Header, o.NullText, o.QuoteAll, o.Crlf, o.Bom
	if o.Table != "" {
		opts.Table = o.Table
	}
	if o.BatchRows > 0 {
		opts.BatchRows = int(o.BatchRows)
	}
	return opts, nil
}

// checkDir fails fast, before any query runs, when the target directory is
// missing or unreadable.
func checkDir(dir string) error {
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return invalid("directory %s does not exist", dir)
	case err != nil:
		return runError(context.Background(), err)
	case !fi.IsDir():
		return invalid("%s is not a directory", dir)
	}
	return nil
}

func invalid(format string, a ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, a...))
}

// runError maps an error from the session, the result or the file.
func runError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		return connect.NewError(connect.CodeInvalidArgument, qe)
	}
	if errors.Is(err, fs.ErrPermission) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(connect.CodeUnavailable, err)
}
