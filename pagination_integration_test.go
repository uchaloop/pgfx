//go:build integration

package pgfx_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
)

// TestFetchPageIntegration walks a table whose sort column repeats, which is
// where a page without a tie order starts losing and duplicating rows.
func TestFetchPageIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	t.Cleanup(cleanup)

	const table = "pgfx_integration_pages"

	if _, err := db.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatalf("drop stale table: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()

		_, _ = db.Exec(cleanupCtx, "DROP TABLE IF EXISTS "+table)
	})

	if _, err := db.Exec(
		ctx,
		`
			CREATE TABLE `+table+` (
				id bigint PRIMARY KEY,
				grade text NOT NULL,
				country text NOT NULL
			)
		`,
	); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Ten matching rows sharing three grades, plus one row the filter excludes.
	if _, err := db.Exec(
		ctx,
		`
			INSERT INTO `+table+` (
				id,
				grade,
				country
			)
			SELECT
				i,
				'g' || (i % 3),
				'TR'
			FROM generate_series(1, 10) AS i
		`,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	if _, err := db.Exec(
		ctx,
		`
			INSERT INTO `+table+` (
				id,
				grade,
				country
			)
			VALUES (
				99,
				'g0',
				'DE'
			)
		`,
	); err != nil {
		t.Fatalf("seed excluded row: %v", err)
	}

	type pageItem struct {
		ID    int64             `db:"id"`
		Grade string            `db:"grade"`
		Meta  map[string]string `db:"meta"`
	}

	order, err := page.Sortable[string]{"grade": "grade"}.OrderBy([]page.Key[string]{{Name: "grade", Desc: true}}, "id")
	if err != nil {
		t.Fatal(err)
	}

	query := `
		SELECT
			id,
			grade,
			jsonb_build_object('country', country) AS meta,
			count(*) OVER () AS total
		FROM ` + table + `
		WHERE country = @country
		ORDER BY ` + order + `
		LIMIT @limit
		OFFSET @offset
	`

	fetch := func(number int) (page.Result[pageItem], error) {
		bounds, err := page.MakeBounds(number, 3)
		if err != nil {
			return page.Result[pageItem]{}, err
		}

		return db.FetchPage[pageItem](ctx, query, pgx.NamedArgs{"country": "TR"}, bounds)
	}

	seen := make(map[int64]int, 10)

	for number := 1; number <= 4; number++ {
		result, err := fetch(number)
		if err != nil {
			t.Fatalf("page %d: %v", number, err)
		}

		want := 3

		if number == 4 {
			want = 1
		}

		if result.Total != 10 || len(result.List) != want {
			t.Fatalf("page %d: rows = %d, total = %d", number, len(result.List), result.Total)
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

	result, err := fetch(9)
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}

	if result.List == nil || len(result.List) != 0 || result.Total != 10 {
		t.Fatalf("result = %+v, want empty page with total", result)
	}
}

func TestFetchPageTotalIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	defer cleanup()

	type item struct {
		ID int64 `db:"id"`
	}

	for _, tc := range []struct {
		name  string
		sql   string
		total int64
	}{
		{
			name: "filter",
			sql: `
				SELECT
					id,
					count(*) OVER () AS total
				FROM generate_series(1::bigint, 10) AS items(id)
				WHERE id > @min
			`,
			total: 5,
		},
		{
			name: "empty",
			sql: `
				SELECT
					id,
					count(*) OVER () AS total
				FROM generate_series(1::bigint, 10) AS items(id)
				WHERE id > @min + 5
			`,
		},
		{
			name: "group",
			sql: `
				SELECT
					id % 3 AS id,
					count(*) OVER () AS total
				FROM generate_series(1::bigint, 10) AS items(id)
				WHERE id > @min
				GROUP BY id % 3
			`,
			total: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, number := range []int{1, 20} {
				bounds, err := page.MakeBounds(number, 2)
				if err != nil {
					t.Fatal(err)
				}

				result, err := db.FetchPage[item](ctx, tc.sql+" ORDER BY id LIMIT @limit OFFSET @offset", pgx.NamedArgs{"min": 5}, bounds)
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

	const query = `
		SELECT
			id
		FROM generate_series(1::bigint, 3) AS items(id)
		LIMIT @limit
		OFFSET @offset
	`

	_, err := db.FetchPage[item](
		ctx,
		query,
		nil,
		page.Bounds{Limit: 2},
	)
	if err == nil || !strings.Contains(err.Error(), "total") {
		t.Fatalf("missing total column: %v", err)
	}
}

func TestFetchPageSortIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	defer cleanup()

	type item struct {
		ID    int64  `db:"id"`
		Score *int64 `db:"score"`
	}

	const sql = `
		SELECT
			id,
			CASE WHEN id % 5 = 0 THEN NULL ELSE id % 3 END AS score
		FROM generate_series(1::bigint, 30) AS items(id)
	`

	for _, tc := range []struct {
		name string
		expr string
		desc bool
	}{
		{name: "ascending", expr: "score"},
		{name: "descending", expr: "score", desc: true},
		{name: "nulls_first", expr: "(score IS NOT NULL), score"},
		{name: "nulls_last", expr: "(score IS NULL), score", desc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order, err := page.Sortable[string]{"score": tc.expr}.OrderBy([]page.Key[string]{{Name: "score", Desc: tc.desc}}, "id")
			if err != nil {
				t.Fatal(err)
			}

			want, err := db.FetchRows[item](ctx, `
				SELECT
					id,
					score
				FROM (`+sql+`) AS items
				ORDER BY `+order+`
			`)
			if err != nil {
				t.Fatal(err)
			}

			query := `
				SELECT
					id,
					score,
					count(*) OVER () AS total
				FROM (` + sql + `) AS items
				ORDER BY ` + order + `
				LIMIT @limit
				OFFSET @offset
			`

			var rows []item

			for number := 1; number <= 5; number++ {
				bounds, err := page.MakeBounds(number, 7)
				if err != nil {
					t.Fatal(err)
				}

				result, err := db.FetchPage[item](ctx, query, nil, bounds)
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

func TestTxFetchPageIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	defer cleanup()

	type item struct {
		ID int64 `db:"id"`
	}

	err := db.Transaction(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx *pgfx.Tx) error {
		const query = `
			SELECT
				id,
				count(*) OVER () AS total
			FROM generate_series(1::bigint, 10) AS items(id)
			ORDER BY id
			LIMIT @limit
			OFFSET @offset
		`

		result, err := tx.FetchPage[item](
			ctx,
			query,
			nil,
			page.Bounds{Limit: 3, Offset: 3},
		)
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
}
