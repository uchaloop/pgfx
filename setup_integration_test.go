//go:build integration

package pgfx_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/uchaloop/pgfx"
	pgfxtest "github.com/uchaloop/pgfx/test"
)

func makeIntegrationDB(t *testing.T, timeout time.Duration) (context.Context, *pgfx.DB, func()) {
	t.Helper()

	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	return ctx, db, func() {
		db.Close()
		cancel()
	}
}
