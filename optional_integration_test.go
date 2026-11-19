//go:build integration

package pgfx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
)

func TestFetchOptionalRowPostgres(t *testing.T) {
	ctx, db, cleanup := makeIntegrationDB(t, 10*time.Second)
	defer cleanup()

	type row struct {
		ID int64 `db:"id"`
	}

	check := func(t *testing.T, fetch func(context.Context, string, ...any) (row, bool, error), strict func(context.Context, string, ...any) (row, error)) {
		t.Helper()

		got, isFound, err := fetch(ctx, "SELECT $1::bigint AS id", int64(7))
		if err != nil || !isFound || got.ID != 7 {
			t.Fatalf(
				"one row: %+v, %v, %v",
				got,
				isFound,
				err,
			)
		}

		got, isFound, err = fetch(ctx, "SELECT 1::bigint AS id WHERE false")
		if err != nil || isFound || got != (row{}) {
			t.Fatalf(
				"empty: %+v, %v, %v",
				got,
				isFound,
				err,
			)
		}

		if _, err := strict(ctx, "SELECT 1::bigint AS id WHERE false"); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("FetchRow contract: %v", err)
		}

		const query = `
			SELECT
				id
			FROM generate_series(1::bigint, 2) AS items(id)
		`

		got, isFound, err = fetch(ctx, query)
		if !errors.Is(err, pgx.ErrTooManyRows) || isFound || got != (row{}) {
			t.Fatalf(
				"many: %+v, %v, %v",
				got,
				isFound,
				err,
			)
		}

		got, isFound, err = fetch(ctx, "SELECT 'not an integer'::text AS id")
		if err == nil || isFound || got != (row{}) {
			t.Fatalf(
				"decode: %+v, %v, %v",
				got,
				isFound,
				err,
			)
		}
	}

	t.Run("pool", func(t *testing.T) { check(t, db.FetchOptionalRow[row], db.FetchRow[row]) })

	err := db.Transaction(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) error {
		t.Run("transaction", func(t *testing.T) { check(t, tx.FetchOptionalRow[row], tx.FetchRow[row]) })

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()

	_, isFound, err := db.FetchOptionalRow[row](canceled, "SELECT 1::bigint AS id")
	if isFound || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v, %v", isFound, err)
	}
}
