//go:build integration

package pgfx_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
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
