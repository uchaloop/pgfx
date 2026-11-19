//go:build integration

package pgfx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
	"github.com/uchaloop/secret/v2"
)

func TestPagePlans(t *testing.T) {
	if os.Getenv("PGFX_PAGE_BENCH") != "1" {
		t.Skip("PGFX_PAGE_BENCH=1 requires the populated pagination-lab stand")
	}

	host := os.Getenv("PGFX_PAGE_TEST_HOST")
	if len(host) == 0 {
		t.Fatal("PGFX_PAGE_TEST_HOST is not set")
	}

	path := os.Getenv("PGFX_PAGE_PLAN_DIR")
	if len(path) == 0 {
		t.Fatal("PGFX_PAGE_PLAN_DIR is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, err := pgfx.MakeDB(ctx, pgfx.Config{
		Host:     host,
		Database: "pagination_lab",
		User:     "pagination_lab",
		Password: secret.New("pagination_lab"),
		TLS:      pgfx.TLSConfig{Mode: "disable"},
		Pool:     pgfx.PoolConfig{MaxConns: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type item struct {
		ID    int64          `db:"id"`
		Score int64          `db:"score"`
		Label string         `db:"label"`
		Meta  map[string]any `db:"meta"`
	}

	type statement struct {
		name string
		sql  string
		args []any
	}

	const (
		windowSQL = `
			SELECT
				id,
				score,
				label,
				meta,
				count(*) OVER () AS total
			FROM bench_rows
			ORDER BY id
			LIMIT @limit
			OFFSET @offset
		`
		rowsSQL = `
			SELECT
				id,
				score,
				label,
				meta
			FROM bench_rows
			ORDER BY id
			LIMIT $1
			OFFSET $2
		`
		countSQL = "SELECT count(*) FROM bench_rows"
	)

	for _, mode := range []struct {
		name       string
		fetch      func(context.Context, page.Bounds) (page.Result[item], error)
		statements func(page.Bounds) []statement
	}{
		{
			name: "window",
			fetch: func(ctx context.Context, b page.Bounds) (page.Result[item], error) {
				return db.FetchPage[item](ctx, windowSQL, nil, b)
			},
			statements: func(b page.Bounds) []statement {
				return []statement{
					{name: "page", sql: windowSQL, args: []any{pgx.NamedArgs{"limit": b.Limit, "offset": b.Offset}}},
				}
			},
		},
		{
			name: "split",
			fetch: func(ctx context.Context, b page.Bounds) (page.Result[item], error) {
				list, err := db.FetchRows[item](ctx, rowsSQL, b.Limit, b.Offset)
				if err != nil {
					return page.Result[item]{}, err
				}

				total, err := db.FetchValue[int64](ctx, countSQL)
				if err != nil {
					return page.Result[item]{}, err
				}

				return page.Result[item]{List: list, Total: total}, nil
			},
			statements: func(b page.Bounds) []statement {
				return []statement{
					{name: "rows", sql: rowsSQL, args: []any{b.Limit, b.Offset}},
					{name: "count", sql: countSQL},
				}
			},
		},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				number int
				count  int
			}{
				{name: "first", number: 1, count: 20},
				{name: "deep", number: 90001, count: 20},
				{name: "empty", number: 100002},
			} {
				t.Run(tc.name, func(t *testing.T) {
					bounds, err := page.MakeBounds(tc.number, 20)
					if err != nil {
						t.Fatal(err)
					}

					var times []float64

					for i := range 8 {
						startedAt := time.Now()
						result, err := mode.fetch(ctx, bounds)
						elapsed := float64(time.Since(startedAt).Nanoseconds()) / 1e6
						if err != nil {
							t.Fatal(err)
						}

						if result.Total != 2000000 || result.List == nil || len(result.List) != tc.count {
							t.Fatalf("count=%d total=%d", len(result.List), result.Total)
						}

						for j, row := range result.List {
							id := bounds.Offset + int64(j) + 1
							if row.ID != id || row.Score != id%100000 || row.Label != fmt.Sprintf("row-%d", id) || len(row.Meta) != 2 {
								t.Fatalf("row=%+v want id=%d", row, id)
							}
						}

						if i > 0 {
							times = append(times, elapsed)
						}
					}

					slices.Sort(times)
					t.Logf("median=%.3fms (page and total)", times[len(times)/2])

					for _, plan := range mode.statements(bounds) {
						raw, err := db.FetchValue[json.RawMessage](ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+plan.sql, plan.args...)
						if err != nil {
							t.Fatal(err)
						}

						var plans []struct {
							Plan map[string]any `json:"Plan"`
						}

						if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 || plans[0].Plan == nil {
							t.Fatalf("invalid explain result: %s, %v", raw, err)
						}

						if err := os.WriteFile(fmt.Sprintf(
							"%s/page-%s-%s-%s.json",
							path,
							mode.name,
							tc.name,
							plan.name,
						), raw, 0644); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		})
	}
}
