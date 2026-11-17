//go:build integration

package pgfx_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
	pgfxtest "github.com/uchaloop/pgfx/test"
)

func TestReadFallbackUnavailableReplica(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is required")
	}
	master, err := pgfxtest.MakeDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	replica, err := pgfx.MakeDB(t.Context(), pgfx.Config{
		Host:     "127.0.0.1:1",
		Database: "postgres",
		TLS:      pgfx.TLSConfig{Mode: "disable"},
		Timeouts: pgfx.TimeoutConfig{Connect: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	calls := 0
	value, err := pgfx.ReadWithFallback(
		t.Context(),
		replica,
		master,
		func(ctx context.Context, db *pgfx.DB) (int, error) {
			calls++
			return db.FetchValue[int](ctx, "SELECT 42")
		},
	)
	if err != nil || value != 42 || calls != 2 {
		t.Fatalf(
			"calls=%d value=%d error=%v",
			calls,
			value,
			err,
		)
	}
	calls = 0
	_, err = pgfx.ReadWithFallback(
		t.Context(),
		master,
		replica,
		func(ctx context.Context, db *pgfx.DB) (int, error) {
			calls++
			return db.FetchValue[int](ctx, "SELECT 1 WHERE false")
		},
	)
	if !errors.Is(err, pgx.ErrNoRows) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestReadFallbackWholePage(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is required")
	}
	replica, err := pgfxtest.MakeDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	master, err := pgfxtest.MakeDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	pager, err := page.MakePager(page.Config{})
	if err != nil {
		t.Fatal(err)
	}
	lists, counts := 0, 0
	result, err := pgfx.ReadWithFallback(
		t.Context(),
		replica,
		master,
		func(ctx context.Context, db *pgfx.DB) (page.Result[int], error) {
			return pager.Fetch(
				ctx,
				db,
				page.Params{},
				func(ctx context.Context, tx *pgfx.Tx, b page.Bounds) ([]int, error) {
					lists++
					value := 1
					if db == master {
						value = 2
					}
					return tx.FetchValues[int](ctx, "SELECT $1::int", value)
				},
				func(ctx context.Context, tx *pgfx.Tx) (int64, error) {
					counts++
					if db == replica {
						return 0, io.EOF
					}
					return tx.FetchValue[int64](ctx, "SELECT 1::bigint")
				},
			)
		},
	)
	if err != nil || lists != 2 || counts != 2 || result.Total != 1 || len(result.List) != 1 || result.List[0] != 2 {
		t.Fatalf(
			"lists=%d counts=%d result=%+v error=%v",
			lists,
			counts,
			result,
			err,
		)
	}
}
