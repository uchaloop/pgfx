package pgfx

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

type pageCountQuerier struct {
	query func(context.Context, string, ...any) (pgx.Rows, error)
}

func (q pageCountQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.query(ctx, sql, args...)
}

func TestPageCountFailureDiscardsRows(t *testing.T) {
	want := errors.New("count failed")

	for _, failure := range []string{"query", "scan", "iteration", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rows := makeWarehouseRows(1)
			count := makeFakeRows([]string{"count"}, []any{int64(10)})
			if failure == "scan" {
				count.scanErr = want
			}

			if failure == "iteration" {
				count.err = want
			}

			calls := 0
			q := pageCountQuerier{query: func(got context.Context, _ string, _ ...any) (pgx.Rows, error) {
				calls++
				if got != ctx {
					t.Fatal("context changed")
				}

				if calls == 1 {
					return rows, nil
				}

				if !rows.isClosed {
					t.Fatal("page rows remain open before count")
				}

				if failure == "query" {
					return nil, want
				}

				if failure == "cancel" {
					cancel()

					return nil, got.Err()
				}

				return count, nil
			}}

			result, err := collectPage[warehouse](
				ctx,
				q,
				makeWarehouseQuery(t),
				page.Request{Page: 1, Limit: 2},
				[]any{"TR"},
			)
			expected := want
			if failure == "cancel" {
				expected = context.Canceled
			}

			if !errors.Is(err, expected) || result.List != nil || result.Total != 0 || calls != 2 {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}

			if (failure == "scan" || failure == "iteration") && !count.isClosed {
				t.Fatal("count rows remain open")
			}
		})
	}
}

func TestPageCountPreservesFilterArgs(t *testing.T) {
	backing := []any{"TR", "keep", "keep"}
	q := &scriptedQuerier{rows: []pgx.Rows{
		makeWarehouseRows(1),
		makeFakeRows([]string{"count"}, []any{int64(10)}),
	}}
	_, err := collectPage[warehouse](
		context.Background(),
		q,
		makeWarehouseQuery(t),
		page.Request{Page: 2, Limit: 2},
		backing[:1],
	)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(backing, []any{"TR", "keep", "keep"}) || !reflect.DeepEqual(q.args[1], []any{"TR"}) {
		t.Fatalf("filter args changed: backing=%v count=%v", backing, q.args[1])
	}

	if !reflect.DeepEqual(q.args[0], []any{"TR", uint(2), uint(2)}) {
		t.Fatalf("page args=%v", q.args[0])
	}

	if q.sql[1] != "SELECT COUNT(*) FROM (SELECT w.id, w.city_eng FROM warehouses w WHERE w.country = $1) AS pgfx_page" {
		t.Fatalf("count SQL=%s", q.sql[1])
	}
}

func TestPageRowFailureSkipsCount(t *testing.T) {
	want := errors.New("page failed")

	for _, failure := range []string{"query", "scan", "iteration"} {
		t.Run(failure, func(t *testing.T) {
			rows := makeWarehouseRows(1)
			if failure == "scan" {
				rows.scanErr = want
			}

			if failure == "iteration" {
				rows.err = want
			}

			calls := 0
			q := pageCountQuerier{query: func(context.Context, string, ...any) (pgx.Rows, error) {
				calls++
				if calls != 1 {
					t.Fatal("count executed after page failure")
				}

				if failure == "query" {
					return nil, want
				}

				return rows, nil
			}}

			result, err := collectPage[warehouse](
				context.Background(),
				q,
				makeWarehouseQuery(t),
				page.Request{Page: 1, Limit: 2},
				[]any{"TR"},
			)
			if !errors.Is(err, want) || result.List != nil || result.Total != 0 || calls != 1 {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}

			if failure != "query" && !rows.isClosed {
				t.Fatal("page rows remain open")
			}
		})
	}
}
