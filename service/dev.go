package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
)

// Dev serves synthetic data to validate the transport and the grid.
type Dev struct {
	// OnReport receives benchmark reports; nil rejects them.
	OnReport func(report string)
}

var _ quarryv1connect.DevServiceHandler = Dev{}

func (d Dev) ReportBench(_ context.Context, req *connect.Request[quarryv1.ReportBenchRequest]) (*connect.Response[quarryv1.ReportBenchResponse], error) {
	if d.OnReport == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("not in benchmark mode"))
	}
	d.OnReport(req.Msg.Report)
	return connect.NewResponse(&quarryv1.ReportBenchResponse{}), nil
}

var devTypes = []string{"int8", "text", "numeric", "timestamptz", "bool", "float8", "jsonb", "bytea"}

func (Dev) StreamRows(ctx context.Context, req *connect.Request[quarryv1.StreamRowsRequest], stream *connect.ServerStream[quarryv1.StreamRowsResponse]) error {
	total, ncols := req.Msg.Rows, int(req.Msg.Columns)
	if total < 0 || ncols < 1 || ncols > 1000 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("rows must be >= 0 and columns in 1..1000"))
	}
	delay := time.Duration(req.Msg.DelayMs) * time.Millisecond

	cols := make([]driver.Column, ncols)
	for i := range cols {
		cols[i] = driver.Column{Name: fmt.Sprintf("col_%d", i+1), Type: devTypes[i%len(devTypes)]}
	}
	msg := &quarryv1.StreamRowsResponse{Columns: ColumnsOf(cols)}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	size := int64(FirstBatch)
	for sent := int64(0); sent < total || msg.Columns != nil; {
		n := min(size, total-sent)
		rows := make([][]any, n)
		for i := range rows {
			id := sent + int64(i)
			row := make([]any, ncols)
			for c := range row {
				row[c] = devValue(devTypes[c%len(devTypes)], id, c, base)
			}
			rows[i] = row
		}
		msg.Rows = RowsOf(rows)
		if err := stream.Send(msg); err != nil {
			return err
		}
		sent += n
		msg = &quarryv1.StreamRowsResponse{}
		size = Batch
		if delay > 0 && sent < total {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func devValue(typ string, id int64, col int, base time.Time) any {
	if (id+int64(col))%17 == 0 {
		return nil
	}
	switch typ {
	case "int8":
		return id
	case "text":
		return fmt.Sprintf("row %d / col %d", id, col)
	case "numeric":
		return driver.Number(fmt.Sprintf("%d.%02d", id*7, id%100))
	case "timestamptz":
		return base.Add(time.Duration(id) * time.Minute)
	case "bool":
		return id%2 == 0
	case "float8":
		return float64(id) / 3
	case "jsonb":
		return driver.JSON(fmt.Sprintf(`{"id":%d,"tags":["a","b"]}`, id))
	default:
		return []byte{byte(id), byte(id >> 8), 0xff}
	}
}
