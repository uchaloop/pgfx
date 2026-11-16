package pgfx

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

func TestPageAlwaysCounts(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("rows=%d", n), func(t *testing.T) {
			data := makeWarehouseRows()

			for i := 0; i < n; i++ {
				data.values = append(data.values, []any{int64(i + 1), "Istanbul"})
			}

			count := makeFakeRows([]string{"count"}, []any{int64(10)})
			driver := &scriptedQuerier{rows: []pgx.Rows{data, count}}
			result, err := collectPage[warehouse](
				context.Background(),
				driver,
				makeWarehouseQuery(t),
				page.Request{Page: 2, Limit: 2},
				[]any{"TR"},
			)
			if err != nil {
				t.Fatal(err)
			}

			if len(result.List) != n || result.Total != 10 || !data.isClosed {
				t.Fatalf("result=%+v closed=%v", result, data.isClosed)
			}

			if !count.isClosed {
				t.Fatal("count rows were not closed")
			}

			if result.List == nil {
				t.Fatal("empty page must be non-nil")
			}

			if !reflect.DeepEqual(driver.args[0], []any{"TR", uint(2), uint(2)}) {
				t.Fatalf("args=%v", driver.args[0])
			}

			if !reflect.DeepEqual(driver.args[1], []any{"TR"}) {
				t.Fatalf("count args=%v", driver.args[1])
			}

			if len(driver.sql) != 2 {
				t.Fatalf("queries=%d; want two", len(driver.sql))
			}
		})
	}
}
