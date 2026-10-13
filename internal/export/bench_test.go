package export

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

var benchCols = []driver.Column{{Name: "id"}, {Name: "name"}, {Name: "amount"}, {Name: "price"}, {Name: "at"}, {Name: "active"}, {Name: "note"}}

// repeatSource yields total rows by serving the same page again, so the
// source itself holds constant memory.
func repeatSource(total int) Source {
	page := make([][]any, pageSize)
	for i := range page {
		page[i] = []any{int64(i), "name " + strings.Repeat("x", i%20), driver.Number("1234.56"), float64(i) / 3, ts, i%2 == 0, nil}
	}
	return func(_ context.Context, n int) ([][]any, error) {
		n = min(n, len(page), total)
		total -= n
		return page[:n], nil
	}
}

func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestConstantMemory checks that the live heap does not grow with the number
// of rows written.
func TestConstantMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	for _, f := range []Format{CSV, JSON, SQLInsert, Markdown} {
		base := liveHeap()
		var peak uint64
		progress := func(rows int64) {
			if rows%50_000 == 0 {
				peak = max(peak, liveHeap())
			}
		}
		if _, err := Write(t.Context(), io.Discard, benchCols, repeatSource(300_000), DefaultOptions(f), progress); err != nil {
			t.Fatal(err)
		}
		if peak > base && peak-base > 1<<20 {
			t.Errorf("%v: live heap grew by %d KB", f, (peak-base)>>10)
		}
	}
}

// BenchmarkWrite1M exports one million rows per op.
func BenchmarkWrite1M(b *testing.B) {
	const rows = 1_000_000
	for _, f := range []Format{CSV, TSV, JSON, NDJSON, SQLInsert, Markdown} {
		b.Run(f.String(), func(b *testing.B) {
			b.ReportAllocs()
			var peak uint64
			for b.Loop() {
				progress := func(n int64) {
					if n%200_000 == 0 {
						var m runtime.MemStats
						runtime.ReadMemStats(&m)
						peak = max(peak, m.HeapInuse)
					}
				}
				if _, err := Write(b.Context(), io.Discard, benchCols, repeatSource(rows), DefaultOptions(f), progress); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(peak)/(1<<20), "peak-heap-MB")
		})
	}
}
