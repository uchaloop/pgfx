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

func TestTransactionValuePostgres(t *testing.T) {
	host := os.Getenv("PGFX_TEST_HOST")
	if len(host) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Setenv("PGFX_TEST_MAX_CONNS", "1")

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if db.Config().MaxConns != 1 {
		t.Fatalf("max connections = %d, want 1", db.Config().MaxConns)
	}

	got, err := db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int, error) {
		_, err := tx.Exec(ctx, "CREATE TEMP TABLE value_test (id int UNIQUE DEFERRABLE INITIALLY DEFERRED) ON COMMIT DROP")
		if err != nil {
			return 0, err
		}

		nested, err := tx.TransactionValue(ctx, func(child *pgfx.Tx) (int, error) {
			return child.FetchValue[int](ctx, "INSERT INTO value_test VALUES (1) RETURNING id")
		})
		if err != nil || nested != 1 {
			t.Fatalf("savepoint: %d, %v", nested, err)
		}

		want := errors.New("rollback requested")
		failed, err := tx.TransactionValue(ctx, func(child *pgfx.Tx) (int, error) {
			if _, err := child.Exec(ctx, "INSERT INTO value_test VALUES (2)"); err != nil {
				return 0, err
			}

			return 99, want
		})
		if failed != 0 || !errors.Is(err, want) {
			t.Fatalf("rollback: %d, %v", failed, err)
		}

		return tx.FetchValue[int](ctx, "SELECT COUNT(*) FROM value_test")
	})
	if err != nil || got != 1 {
		t.Fatalf("commit: %d, %v", got, err)
	}

	got, err = db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int, error) {
		_, err := tx.Exec(ctx, "CREATE TEMP TABLE value_commit_failure (id int UNIQUE DEFERRABLE INITIALLY DEFERRED) ON COMMIT DROP; INSERT INTO value_commit_failure VALUES (1), (1)")

		return 99, err
	})
	if got != 0 || !pgfx.IsUniqueViolation(err) {
		t.Fatalf("commit failure: %d, %v", got, err)
	}

	// Keep the fixture on one pooled connection and create it outside the
	// transactions under test so rollback cannot remove the table itself.
	if _, err := db.Exec(ctx, "CREATE TEMP TABLE value_rollback (id int PRIMARY KEY); INSERT INTO value_rollback VALUES (0)"); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if recover() != "transaction panic" {
				t.Fatal("panic was not preserved")
			}
		}()

		_, _ = db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int, error) {
			if _, err := tx.Exec(ctx, "INSERT INTO value_rollback VALUES (1)"); err != nil {
				t.Fatal(err)
			}

			panic("transaction panic")
		})
	}()

	rows, err := db.FetchValues[int](ctx, "SELECT id FROM value_rollback ORDER BY id")
	if err != nil || len(rows) != 1 || rows[0] != 0 {
		t.Fatalf("rows after panic: %v, %v", rows, err)
	}

	want := errors.New("callback failed")
	got, err = db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int, error) {
		if _, err := tx.Exec(ctx, "INSERT INTO value_rollback VALUES (2)"); err != nil {
			return 0, err
		}

		return 99, want
	})
	if got != 0 || !errors.Is(err, want) {
		t.Fatalf("callback error: %d, %v", got, err)
	}

	rows, err = db.FetchValues[int](ctx, "SELECT id FROM value_rollback ORDER BY id")
	if err != nil || len(rows) != 1 || rows[0] != 0 {
		t.Fatalf("rows after callback error: %v, %v", rows, err)
	}

	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()

	got, err = db.TransactionValue(canceled, pgx.TxOptions{}, func(*pgfx.Tx) (int, error) {
		t.Fatal("callback called after failed begin")

		return 99, nil
	})
	if got != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled begin: %d, %v", got, err)
	}
}
