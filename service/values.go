// Package service implements the Connect services on top of internal/.
package service

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/internal/driver"
)

// Batch sizes for streamed rows: a small first batch so the grid paints
// quickly, then larger ones for throughput.
const (
	FirstBatch = 200
	Batch      = 2000
)

// ValueOf converts a normalized driver value (see driver.Result) to protobuf.
func ValueOf(v any) *quarryv1.Value {
	switch v := v.(type) {
	case nil:
		return &quarryv1.Value{Kind: &quarryv1.Value_Null{Null: true}}
	case bool:
		return &quarryv1.Value{Kind: &quarryv1.Value_Bool{Bool: v}}
	case int64:
		return &quarryv1.Value{Kind: &quarryv1.Value_Int{Int: v}}
	case float64:
		return &quarryv1.Value{Kind: &quarryv1.Value_Float{Float: v}}
	case driver.Number:
		return &quarryv1.Value{Kind: &quarryv1.Value_Number{Number: string(v)}}
	case string:
		return &quarryv1.Value{Kind: &quarryv1.Value_Text{Text: v}}
	case []byte:
		return &quarryv1.Value{Kind: &quarryv1.Value_Bytes{Bytes: v}}
	case time.Time:
		return &quarryv1.Value{Kind: &quarryv1.Value_Time{Time: timestamppb.New(v)}}
	case driver.JSON:
		return &quarryv1.Value{Kind: &quarryv1.Value_Json{Json: string(v)}}
	case []any:
		l := &quarryv1.ValueList{Values: make([]*quarryv1.Value, len(v))}
		for i, e := range v {
			l.Values[i] = ValueOf(e)
		}
		return &quarryv1.Value{Kind: &quarryv1.Value_Array{Array: l}}
	default:
		return &quarryv1.Value{Kind: &quarryv1.Value_Text{Text: fmt.Sprint(v)}}
	}
}

// RowsOf converts a page of driver rows.
func RowsOf(rows [][]any) []*quarryv1.Row {
	out := make([]*quarryv1.Row, len(rows))
	for i, r := range rows {
		vals := make([]*quarryv1.Value, len(r))
		for j, v := range r {
			vals[j] = ValueOf(v)
		}
		out[i] = &quarryv1.Row{Values: vals}
	}
	return out
}

func ColumnsOf(cols []driver.Column) []*quarryv1.Column {
	out := make([]*quarryv1.Column, len(cols))
	for i, c := range cols {
		out[i] = &quarryv1.Column{Name: c.Name, Type: c.Type}
	}
	return out
}
