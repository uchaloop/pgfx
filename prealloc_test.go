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
		for _, count := range []int{0, size} {
			b.Run(fmt.Sprintf("size=%d/rows=%d", size, count), func(b *testing.B) {
				rows := makeWarehouseRows()

				for i := 0; i < count; i++ {
					rows.values = append(rows.values, []any{int64(i + 1), "Istanbul"})
				}

				q := fakeQuerier{rows: rows}
				st := page.Statement{Rows: "SELECT id,city_eng FROM warehouses", Limit: uint(size)}
				b.ReportAllocs()
				b.ResetTimer()

				for b.Loop() {
					rows.index, rows.isClosed = 0, false
					result, err := collectPageRows[warehouse](
						context.Background(),
						q,
						st,
						nil,
					)
					if err != nil || len(result) != min(count, size) {
						b.Fatalf(
							"len=%d err=%v",
							len(result),
							err,
						)
					}
				}
			})
		}
	}
}

func TestPagePreallocationFirstRowErrors(t *testing.T) {
	want := errors.New("read failed")

	for _, isScanFailure := range []bool{false, true} {
		rows := makeWarehouseRows(1)
		if isScanFailure {
			rows.scanErr = want
		} else {
			rows.err = want
		}

		result, err := collectPageRows[warehouse](
			context.Background(),
			fakeQuerier{rows: rows},
			page.Statement{Rows: "query", Limit: 20},
			nil,
		)
		if !errors.Is(err, want) || result != nil || !rows.isClosed {
			t.Fatalf(
				"rows=%v closed=%v err=%v",
				result,
				rows.isClosed,
				err,
			)
		}
	}
}

func TestFetchIntoAppendsAndReuses(t *testing.T) {
	data := makeWarehouseRows(2, 3)
	tx := makeTx(pageRowsTx{querier: &scriptedQuerier{rows: []pgx.Rows{data}}})
	dst := make([]warehouse, 1, 3)
	dst[0].ID = 1
	result, err := tx.FetchRowsInto(context.Background(), dst, "query")
	if err != nil || len(result) != 3 || result[0].ID != 1 || result[2].ID != 3 || &result[0] != &dst[0] || !data.isClosed {
		t.Fatalf(
			"result=%v closed=%v err=%v",
			result,
			data.isClosed,
			err,
		)
	}

	values := makeFakeRows([]string{"id"}, []any{int64(7)}, []any{int64(8)})
	tx = makeTx(pageRowsTx{querier: &scriptedQuerier{rows: []pgx.Rows{values}}})
	buffer := make([]int64, 3)
	got, err := tx.FetchValuesInto(context.Background(), buffer[:0], "query")
	if err != nil || len(got) != 2 || got[0] != 7 || got[1] != 8 || &got[0] != &buffer[0] {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestFetchIntoGrowsAndClosesOnError(t *testing.T) {
	rows := makeFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)})
	got, err := collectInto(
		context.Background(),
		fakeQuerier{rows: rows},
		make([]int64, 0, 1),
		pgx.RowTo[int64],
		"query",
	)
	if err != nil || len(got) != 2 || !rows.isClosed {
		t.Fatalf("got=%v err=%v", got, err)
	}

	bad := makeFakeRows([]string{"id"}, []any{int64(1)}, []any{"invalid"})
	buffer := make([]int64, 0, 2)
	got, err = collectInto(
		context.Background(),
		fakeQuerier{rows: bad},
		buffer,
		pgx.RowTo[int64],
		"query",
	)
	if err == nil || got != nil || !bad.isClosed || buffer[:1][0] != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
