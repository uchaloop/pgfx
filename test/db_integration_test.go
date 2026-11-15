//go:build integration

package pgfxtest

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/pgfx"
)

func TestMakeDBPostgres(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := MakeDB(ctx, pgfx.WithAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SELECT set_config('application_name', 'pgfx_test_helper', false)")

		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	got, err := db.FetchValue[string](ctx, "SHOW application_name")
	if err != nil || got != "pgfx_test_helper" {
		t.Fatalf("hook: %q, %v", got, err)
	}
}

func TestMakeDBPingFailure(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	want := errors.New("test hook failed")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := MakeDB(ctx, pgfx.WithAfterConnect(func(context.Context, *pgx.Conn) error { return want }))
	if db != nil {
		db.Close()
		t.Fatal("failed pool returned")
	}

	if !errors.Is(err, want) {
		t.Fatalf("MakeDB: %v", err)
	}
}

func TestMakeDBCancelled(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := MakeDB(ctx)
	if db != nil {
		db.Close()
		t.Fatal("pool returned with canceled context")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MakeDB: %v", err)
	}
}

func TestMakeDBMaxConns(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	defaults, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		value string
		want  int32
	}{
		{value: "", want: defaults.MaxConns},
		{value: "0", want: defaults.MaxConns},
		{value: "1", want: 1},
		{value: "2", want: 2},
	} {
		t.Run("limit_"+item.value, func(t *testing.T) {
			t.Setenv("PGFX_TEST_MAX_CONNS", item.value)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			db, err := MakeDB(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			got := db.Config().MaxConns
			if got != item.want {
				t.Fatalf(
					"max connections for %q: %d, want %d",
					item.value,
					got,
					item.want,
				)
			}
		})
	}
}
