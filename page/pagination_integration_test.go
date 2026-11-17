//go:build integration

package page_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
	pgfxtest "github.com/uchaloop/pgfx/test"
)

func TestFetchPagePostgres(t *testing.T) {
	pager, err := page.MakePager(page.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is required")
	}
	db, err := pgfxtest.MakeDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	type item struct {
		ID int64 `db:"id" json:"-"`
	}
	for _, number := range []int{1, 2, 9} {
		result, err := pager.Fetch(
			t.Context(),
			db,
			page.Params{Page: number, Limit: 2},
			func(ctx context.Context, tx *pgfx.Tx, bounds page.Bounds) ([]item, error) {
				mode, err := tx.FetchValue[string](ctx, "SHOW transaction_isolation")
				if err != nil || mode != "repeatable read" {
					t.Fatalf("isolation %q: %v", mode, err)
				}
				mode, err = tx.FetchValue[string](ctx, "SHOW transaction_read_only")
				if err != nil || mode != "on" {
					t.Fatalf("read only %q: %v", mode, err)
				}
				return tx.FetchRows[item](
					ctx,
					"SELECT id FROM generate_series(1::bigint, 3) AS s(id) ORDER BY id LIMIT $1 OFFSET $2",
					bounds.Limit,
					bounds.Offset,
				)
			},
			func(ctx context.Context, tx *pgfx.Tx) (int64, error) {
				return tx.FetchValue[int64](ctx, "SELECT 3::bigint")
			},
		)
		if err != nil || result.Total != 3 || result.List == nil {
			t.Fatalf("result %+v: %v", result, err)
		}
		if number == 1 && (len(result.List) != 2 || result.List[0].ID != 1) {
			t.Fatal("db mapping or first page changed")
		}
		if number == 2 && (len(result.List) != 1 || result.List[0].ID != 3) {
			t.Fatal("offset changed")
		}
		if number == 9 && len(result.List) != 0 {
			t.Fatal("expected empty page")
		}
	}
	failure := errors.New("callback failed")
	for _, isListFailure := range []bool{true, false} {
		calls := 0
		result, err := pager.Fetch(
			t.Context(),
			db,
			page.Params{},
			func(context.Context, *pgfx.Tx, page.Bounds) ([]item, error) {
				if isListFailure {
					return nil, failure
				}
				return []item{{ID: 1}}, nil
			},
			func(context.Context, *pgfx.Tx) (int64, error) { calls++; return 0, failure },
		)
		if !errors.Is(err, failure) || result.List != nil || result.Total != 0 {
			t.Fatalf("result %+v: %v", result, err)
		}
		if isListFailure && calls != 0 {
			t.Fatal("count called after list failure")
		}
	}
}

func TestFetchPageCancellation(t *testing.T) {
	pager, err := page.MakePager(page.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is required")
	}
	db, err := pgfxtest.MakeDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	for _, stage := range []string{"before", "list", "count"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if stage == "before" {
				cancel()
			}
			calls := 0
			result, err := pager.Fetch(
				ctx,
				db,
				page.Params{},
				func(context.Context, *pgfx.Tx, page.Bounds) ([]int, error) {
					calls++
					if stage == "list" {
						cancel()
					}
					return []int{1}, nil
				},
				func(context.Context, *pgfx.Tx) (int64, error) {
					calls++
					cancel()
					return 1, nil
				},
			)
			wantCalls := map[string]int{"before": 0, "list": 1, "count": 2}[stage]
			if !errors.Is(err, context.Canceled) || result.List != nil || result.Total != 0 || calls != wantCalls {
				t.Fatalf("result %+v, calls %d: %v", result, calls, err)
			}
		})
	}
}

func TestFetchPageCommitFailure(t *testing.T) {
	pager, err := page.MakePager(page.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is required")
	}
	t.Setenv("PGFX_TEST_MAX_CONNS", "1")
	db, err := pgfxtest.MakeDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	// Временная таблица доступна для записи в read-only транзакции.
	// Отложенное ограничение гарантирует ошибку именно при COMMIT.
	if _, err := db.Exec(t.Context(), "CREATE TEMP TABLE page_commit_failure (id int UNIQUE DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	result, err := pager.Fetch(
		t.Context(),
		db,
		page.Params{},
		func(ctx context.Context, tx *pgfx.Tx, _ page.Bounds) ([]int, error) {
			calls++
			if _, err := tx.Exec(ctx, "INSERT INTO page_commit_failure VALUES (1), (1)"); err != nil {
				return nil, err
			}
			return []int{1}, nil
		},
		func(context.Context, *pgfx.Tx) (int64, error) { calls++; return 2, nil },
	)
	if !pgfx.IsUniqueViolation(err) || result.List != nil || result.Total != 0 || calls != 2 {
		t.Fatalf("result %+v, calls %d: %v", result, calls, err)
	}
	count, err := db.FetchValue[int64](t.Context(), "SELECT COUNT(*) FROM page_commit_failure")
	if err != nil || count != 0 {
		t.Fatalf("rollback count %d: %v", count, err)
	}
}
