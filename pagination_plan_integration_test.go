//go:build integration

package pgfx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

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

	db, err := MakeDB(ctx, Config{
		Host:     host,
		Database: "pagination_lab",
		User:     "pagination_lab",
		Password: secret.New("pagination_lab"),
		TLS:      TLSConfig{Mode: "disable"},
		Pool:     PoolConfig{MaxConns: 1},
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
	query := page.MakeQueryOrPanic("SELECT id, score, label, meta FROM bench_rows", page.WithTie(page.MakeAscOrder("id")))

	for _, tc := range []struct {
		name  string
		page  uint
		count int
	}{
		{name: "first", page: 1, count: 20},
		{name: "deep", page: 90001, count: 20},
		{name: "empty", page: 100002},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := page.Request{Page: tc.page, Limit: 20}
			var times []float64

			for i := 0; i < 8; i++ {
				startedAt := time.Now()
				result, err := db.FetchPage[item](ctx, query, req)
				elapsed := float64(time.Since(startedAt).Nanoseconds()) / 1e6
				if err != nil {
					t.Fatal(err)
				}

				if result.Total != 2000000 || result.List == nil || len(result.List) != tc.count {
					t.Fatalf("count=%d total=%d", len(result.List), result.Total)
				}

				for j, row := range result.List {
					id := int64((tc.page-1)*20) + int64(j) + 1
					if row.ID != id || row.Score != id%100000 || row.Label != fmt.Sprintf("row-%d", id) || len(row.Meta) != 2 {
						t.Fatalf("row=%+v want id=%d", row, id)
					}
				}

				if i > 0 {
					times = append(times, elapsed)
				}
			}

			slices.Sort(times)
			t.Logf("median=%.3fms (page and count)", times[len(times)/2])

			statement, err := query.BuildStatement(reflect.TypeFor[item](), req, 0)
			if err != nil {
				t.Fatal(err)
			}

			for _, plan := range []struct {
				name string
				sql  string
				args []any
			}{
				{name: "rows", sql: statement.Rows, args: statement.PageArgs},
				{name: "count", sql: statement.Count},
			} {
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

				if err := os.WriteFile(fmt.Sprintf("%s/page-%s-%s.json", path, tc.name, plan.name), raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
