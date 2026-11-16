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

// TestPageFetchIntegration walks a table whose sort column repeats, which is
// where a page without a tie order starts losing and duplicating rows.
func TestPageFetchIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
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

	type pageItem struct {
		ID    int64             `db:"id"    json:"id"`
		Grade string            `db:"grade" json:"grade"`
		Meta  map[string]string `db:"meta"`
	}

	fetch := func(ctx context.Context, offset int) (page.Result[pageItem], error) {
		return page.Fetch(
			ctx,
			func(ctx context.Context) ([]pageItem, error) {
				return db.FetchRows[pageItem](
					ctx,
					"SELECT id, grade, jsonb_build_object('country', country) AS meta FROM "+table+" WHERE country = $1 ORDER BY grade DESC, id ASC LIMIT $2 OFFSET $3",
					"TR",
					3,
					offset,
				)
			},
			func(ctx context.Context) (int64, error) {
				return db.FetchValue[int64](ctx, "SELECT COUNT(*) FROM "+table+" WHERE country = $1", "TR")
			},
		)
	}

	seen := make(map[int64]int, 10)

	for number := uint(1); number <= 4; number++ {
		result, err := fetch(ctx, int(number-1)*3)
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

	result, err := fetch(ctx, 24)
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}

	if result.List == nil || len(result.List) != 0 || result.Total != 10 {
		t.Fatalf("result = %+v, want empty last page", result)
	}
}

func TestPageCountIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
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
			for _, number := range []uint{1, 20} {
				result, err := page.Fetch(
					ctx,
					func(ctx context.Context) ([]item, error) {
						return db.FetchRows[item](
							ctx,
							tc.sql+" ORDER BY id LIMIT $2 OFFSET $3",
							tc.arg,
							2,
							int(number-1)*2,
						)
					},
					func(ctx context.Context) (int64, error) {
						return db.FetchValue[int64](ctx, "SELECT COUNT(*) FROM ("+tc.sql+") AS counted", tc.arg)
					},
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

	err := db.Transaction(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pgfx.Tx) error {
		result, err := page.Fetch(
			ctx,
			func(ctx context.Context) ([]item, error) {
				return tx.FetchRows[item](
					ctx,
					"SELECT id FROM generate_series(1::bigint, 10) AS items(id) ORDER BY id LIMIT $1 OFFSET $2",
					3,
					3,
				)
			},
			func(ctx context.Context) (int64, error) {
				return tx.FetchValue[int64](ctx, "SELECT COUNT(*) FROM generate_series(1, 10)")
			},
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

func TestPageSortIntegration(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
	ctx, db, cleanup := makeIntegrationDB(t, 20*time.Second)
	defer cleanup()

	type item struct {
		ID    int64  `db:"id"`
		Score *int64 `db:"score"`
	}
	const sql = "SELECT id, CASE WHEN id % 5 = 0 THEN NULL ELSE id % 3 END AS score FROM generate_series(1::bigint, 30) AS items(id)"

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{name: "ascending", sql: "score ASC, id ASC"},
		{name: "descending", sql: "score DESC, id ASC"},
		{
			name: "nulls_first",
			sql:  "score ASC NULLS FIRST, id ASC",
		},
		{
			name: "nulls_last",
			sql:  "score DESC NULLS LAST, id ASC",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := db.FetchRows[item](ctx, sql+" ORDER BY "+tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			var rows []item

			for number := uint(1); number <= 5; number++ {
				result, err := page.Fetch(
					ctx,
					func(ctx context.Context) ([]item, error) {
						return db.FetchRows[item](
							ctx,
							sql+" ORDER BY "+tc.sql+" LIMIT $1 OFFSET $2",
							7,
							int(number-1)*7,
						)
					},
					func(ctx context.Context) (int64, error) {
						return db.FetchValue[int64](ctx, "SELECT COUNT(*) FROM generate_series(1, 30)")
					},
				)
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

func TestPageParallelPostgres(t *testing.T) {
	t.Setenv("PGFX_TEST_MAX_CONNS", "2")
	ctx, db, cleanup := makeIntegrationDB(t, 10*time.Second)
	defer cleanup()

	type item struct {
		ID int64 `db:"id"`
	}

	listReady, countReady := make(chan struct{}), make(chan struct{})
	var listPID, countPID uint32

	result, err := page.Fetch(
		ctx,
		func(ctx context.Context) ([]item, error) {
			conn, err := db.Acquire(ctx)
			if err != nil {
				return nil, err
			}
			defer conn.Release()

			listPID = conn.Conn().PgConn().PID()
			close(listReady)

			select {
			case <-countReady:
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			rows, err := conn.Query(
				ctx,
				"SELECT id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1 ORDER BY id LIMIT $2 OFFSET $3",
				5,
				2,
				1,
			)
			if err != nil {
				return nil, err
			}

			return pgx.CollectRows(rows, pgx.RowToStructByNameLax[item])
		},
		func(ctx context.Context) (int64, error) {
			conn, err := db.Acquire(ctx)
			if err != nil {
				return 0, err
			}
			defer conn.Release()

			countPID = conn.Conn().PgConn().PID()
			close(countReady)

			select {
			case <-listReady:
			case <-ctx.Done():
				return 0, ctx.Err()
			}

			var total int64

			if err := conn.QueryRow(ctx, "SELECT COUNT(*) FROM generate_series(1, 10) AS items(id) WHERE id >= $1", 6).Scan(&total); err != nil {
				return 0, err
			}

			return total, nil
		},
		page.WithParallel(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if listPID == countPID || result.Total != 5 || len(result.List) != 2 || result.List[0].ID != 7 || result.List[1].ID != 8 {
		t.Fatalf(
			"result=%+v pids=%d,%d",
			result,
			listPID,
			countPID,
		)
	}
}
