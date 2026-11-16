//go:build integration

package pgfx_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
)

type pageItem struct {
	ID    int64             `db:"id"    json:"id"`
	Grade string            `db:"grade" json:"grade"`
	Meta  map[string]string `db:"meta"`
}

// TestFetchPageIntegration walks a table whose sort column repeats, which is
// where a page without a tie order starts losing and duplicating rows.
func TestFetchPageIntegration(t *testing.T) {
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	t.Cleanup(cleanup)

	const table = "pgfx_integration_pages"

	if _, err := db.Exec(ctx, "drop table if exists "+table); err != nil {
		t.Fatalf("drop stale table: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()

		_, _ = db.Exec(cleanupCtx, "drop table if exists "+table)
	})

	if _, err := db.Exec(
		ctx,
		"create table "+table+" (id bigint primary key, grade text not null, country text not null)",
	); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Ten matching rows sharing three grades, plus one row the filter excludes.
	if _, err := db.Exec(
		ctx,
		"insert into "+table+" (id, grade, country) "+
			"select i, 'g' || (i % 3), 'TR' from generate_series(1, 10) AS i",
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	if _, err := db.Exec(
		ctx,
		"insert into "+table+" (id, grade, country) values (99, 'g0', 'DE')",
	); err != nil {
		t.Fatalf("seed excluded row: %v", err)
	}

	query, err := page.MakeQuery(
		"SELECT id, grade, jsonb_build_object('country', country) AS meta FROM "+table+" WHERE country = $1;",
		page.WithTie(page.MakeAscOrder("id")),
		page.WithSortKeyTag("json"),
	)
	if err != nil {
		t.Fatalf("page.MakeQuery: %v", err)
	}

	seen := make(map[int64]int, 10)
	sort := []string{"grade:desc"}

	for number := uint(1); number <= 4; number++ {
		req := page.Request{Page: number, Limit: 3, Sort: sort}

		result, err := db.FetchPage[pageItem](
			ctx,
			query,
			req,
			"TR",
		)
		if err != nil {
			t.Fatalf("page %d: %v", number, err)
		}

		if result.Total != 10 {
			t.Fatalf("page %d: total=%v", number, result.Total)
		}

		want := 3
		if number == 4 {
			want = 1
		}

		if len(result.List) != want {
			t.Fatalf(
				"page %d: rows = %d, want %d",
				number,
				len(result.List),
				want,
			)
		}

		for _, item := range result.List {
			seen[item.ID]++
			if item.Meta["country"] != "TR" {
				t.Fatalf("map decoding: %+v", item)
			}
		}
	}

	if len(seen) != 10 {
		t.Fatalf("distinct rows = %d, want 10", len(seen))
	}

	for id, times := range seen {
		if times != 1 {
			t.Fatalf("row %d appeared %d times", id, times)
		}
	}

	result, err := db.FetchPage[pageItem](
		ctx,
		query,
		page.Request{Page: 9, Limit: 3, Sort: sort},
		"TR",
	)
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}

	if result.List == nil || len(result.List) != 0 || result.Total != 10 {
		t.Fatalf("result = %+v, want empty last page", result)
	}

}

func TestPageCountIntegration(t *testing.T) {
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	defer cleanup()

	type item struct {
		ID int64 `db:"id"`
	}

	for _, tc := range []struct {
		name  string
		sql   string
		arg   int64
		total int64
	}{
		{
			name:  "filter",
			sql:   "SELECT id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1",
			arg:   5,
			total: 5,
		},
		{
			name: "empty",
			sql:  "SELECT id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1",
			arg:  10,
		},
		{
			name:  "distinct",
			sql:   "SELECT DISTINCT id % 3 AS id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1",
			arg:   5,
			total: 3,
		},
		{
			name:  "group",
			sql:   "SELECT id % 3 AS id FROM generate_series(1::bigint, 10) AS items(id) GROUP BY id % 3 HAVING count(*) > $1",
			arg:   3,
			total: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := page.MakeQueryOrPanic(tc.sql, page.WithTie(page.MakeAscOrder("id")))

			for _, number := range []uint{1, 20} {
				result, err := db.FetchPage[item](
					ctx,
					query,
					page.Request{Page: number, Limit: 2},
					tc.arg,
				)
				if err != nil {
					t.Fatal(err)
				}

				want := int(min(tc.total, 2))
				if number == 20 {
					want = 0
				}

				if result.Total != tc.total || result.List == nil || len(result.List) != want {
					t.Fatalf("page %d: %+v", number, result)
				}
			}
		})
	}

	query := page.MakeQueryOrPanic("SELECT id FROM generate_series(1::bigint, 10) AS items(id)", page.WithTie(page.MakeAscOrder("id")))
	err := db.Transaction(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pgfx.Tx) error {
		result, err := tx.FetchPage[item](ctx, query, page.Request{Page: 2, Limit: 3})
		if err != nil {
			return err
		}

		if result.Total != 10 || len(result.List) != 3 || result.List[0].ID != 4 {
			t.Fatalf("transaction result: %+v", result)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.FetchPage[item](canceled, query, page.Request{Page: 1, Limit: 2}); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestPageSortIntegration(t *testing.T) {
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	defer cleanup()

	type item struct {
		ID    int64  `db:"id"`
		Score *int64 `db:"score"`
	}
	const sql = "SELECT id, CASE WHEN id % 5 = 0 THEN NULL ELSE id % 3 END AS score FROM generate_series(1::bigint, 30) AS items(id)"

	for _, tc := range []struct {
		name  string
		order page.Order
		sql   string
	}{
		{name: "ascending", order: page.MakeAscOrder("score"), sql: "score ASC, id ASC"},
		{name: "descending", order: page.MakeDescOrder("score"), sql: "score DESC, id ASC"},
		{
			name:  "nulls_first",
			order: page.Order{Column: "score", Nulls: page.NullsFirst},
			sql:   "score ASC NULLS FIRST, id ASC",
		},
		{
			name:  "nulls_last",
			order: page.Order{Column: "score", IsDesc: true, Nulls: page.NullsLast},
			sql:   "score DESC NULLS LAST, id ASC",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := page.MakeQueryOrPanic(sql, page.WithHead(tc.order, page.MakeAscOrder("id")), page.WithTie(page.MakeAscOrder("id")))
			want, err := db.FetchRows[item](ctx, sql+" ORDER BY "+tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			var rows []item

			for number := uint(1); number <= 5; number++ {
				result, err := db.FetchPage[item](ctx, query, page.Request{Page: number, Limit: 7})
				if err != nil {
					t.Fatal(err)
				}

				if result.Total != 30 {
					t.Fatalf("total=%d", result.Total)
				}

				rows = append(rows, result.List...)
			}

			if !reflect.DeepEqual(rows, want) {
				t.Fatalf("rows=%+v want=%+v", rows, want)
			}
		})
	}
}
