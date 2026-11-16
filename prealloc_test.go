package pgfx

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestFetchIntoAppendsAndReuses(t *testing.T) {
	data := makeWarehouseRows(2, 3)
	tx := makeTx(&fakeNativeTx{rows: data})
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
	tx = makeTx(&fakeNativeTx{rows: values})
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
