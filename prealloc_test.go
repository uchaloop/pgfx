package pgfx

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

func BenchmarkCollectPageRowsCapacity(b *testing.B) {
	for _, size := range []int{20, 100, 1000} {
		for _, count := range []int{0, size + 1} {
			b.Run(fmt.Sprintf("size=%d/rows=%d", size, count), func(b *testing.B) {
				rows := warehouseRows()
				for i := 0; i < count; i++ {
					rows.values = append(rows.values, []any{int64(i + 1), "Istanbul"})
				}
				q := fakeQuerier{rows: rows}
				st := page.Statements{Rows: "SELECT id,city_eng FROM warehouses", Limit: uint(size)}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					rows.index, rows.closed = 0, false
					result, more, err := collectPageRows[warehouse](context.Background(), q, st, nil)
					if err != nil || len(result) != min(count, size) || more != (count > size) {
						b.Fatalf("len=%d more=%v err=%v", len(result), more, err)
					}
				}
			})
		}
	}
}

func TestPagePreallocationUsesRequestedSize(t *testing.T) {
	for _, count := range []int{0, 1} {
		rows := warehouseRows()
		if count != 0 {
			rows = warehouseRows(1)
		}
		result, more, err := collectPageRows[warehouse](context.Background(), fakeQuerier{rows: rows}, page.Statements{Rows: "query", Limit: 1000}, nil)
		if err != nil || more || result == nil || len(result) != count || cap(result) != 1001 || !rows.closed {
			t.Fatalf("len=%d cap=%d more=%v closed=%v err=%v", len(result), cap(result), more, rows.closed, err)
		}
	}
}

func TestPagePreallocationFirstRowErrors(t *testing.T) {
	want := errors.New("read failed")
	for _, scanFailure := range []bool{false, true} {
		rows := warehouseRows(1)
		if scanFailure {
			rows.scanErr = want
		} else {
			rows.err = want
		}
		result, more, err := collectPageRows[warehouse](context.Background(), fakeQuerier{rows: rows}, page.Statements{Rows: "query", Limit: 20}, nil)
		if !errors.Is(err, want) || result != nil || more || !rows.closed {
			t.Fatalf("rows=%v more=%v closed=%v err=%v", result, more, rows.closed, err)
		}
	}
}

func TestFetchIntoAppendsAndReuses(t *testing.T) {
	data := warehouseRows(2, 3)
	tx := wrapTx(pageRowsTx{querier: &scriptedQuerier{rows: []pgx.Rows{data}}})
	dst := make([]warehouse, 1, 3)
	dst[0].ID = 1
	result, err := tx.FetchRowsInto(context.Background(), dst, "query")
	if err != nil || len(result) != 3 || result[0].ID != 1 || result[2].ID != 3 || &result[0] != &dst[0] || !data.closed {
		t.Fatalf("result=%v closed=%v err=%v", result, data.closed, err)
	}
	values := newFakeRows([]string{"id"}, []any{int64(7)}, []any{int64(8)})
	tx = wrapTx(pageRowsTx{querier: &scriptedQuerier{rows: []pgx.Rows{values}}})
	buffer := make([]int64, 3)
	got, err := tx.FetchValuesInto(context.Background(), buffer[:0], "query")
	if err != nil || len(got) != 2 || got[0] != 7 || got[1] != 8 || &got[0] != &buffer[0] {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestFetchIntoGrowsAndClosesOnError(t *testing.T) {
	rows := newFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)})
	got, err := collectInto(context.Background(), fakeQuerier{rows: rows}, make([]int64, 0, 1), pgx.RowTo[int64], "query")
	if err != nil || len(got) != 2 || !rows.closed {
		t.Fatalf("got=%v err=%v", got, err)
	}
	bad := newFakeRows([]string{"id"}, []any{int64(1)}, []any{"invalid"})
	buffer := make([]int64, 0, 2)
	got, err = collectInto(context.Background(), fakeQuerier{rows: bad}, buffer, pgx.RowTo[int64], "query")
	if err == nil || got != nil || !bad.closed || buffer[:1][0] != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
