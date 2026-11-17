//go:build integration

package pgfx

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/secret/v2"
	"go.uber.org/fx"
)

func TestPostgresIntegration(t *testing.T) {
	cfg := makeIntegrationConfig(t, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := MakeDB(ctx, cfg)
	if err != nil {
		t.Fatalf("MakeDB: %v", err)
	}

	pool := db.Pool
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	one, err := db.FetchValue[int](ctx, "select 1")
	if err != nil {
		t.Fatalf("FetchValue: %v", err)
	}

	if one != 1 {
		t.Fatalf("FetchValue = %d, want 1", one)
	}

	const table = "pgfx_integration_items"

	if _, err := pool.Exec(ctx, "drop table if exists "+table); err != nil {
		t.Fatalf("drop stale table: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()

		_, _ = pool.Exec(cleanupCtx, "drop table if exists "+table)
	})
	if _, err := pool.Exec(
		ctx,
		"create table "+table+" (id bigint primary key)",
	); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := db.Transaction(ctx, pgx.TxOptions{}, func(tx *Tx) error {
		_, err := tx.Exec(ctx, "insert into "+table+" (id) values (1)")
		if err != nil {
			return err
		}

		count, err := tx.FetchValue[int64](ctx, "select count(*) from "+table)
		if err != nil {
			return err
		}

		if count != 1 {
			return errors.New("insert is not visible inside transaction")
		}

		return nil
	}); err != nil {
		t.Fatalf("committed Tx: %v", err)
	}

	requireIntegrationCount(
		t,
		ctx,
		db,
		table,
		1,
	)

	errRollback := errors.New("rollback requested")
	err = db.Transaction(ctx, pgx.TxOptions{}, func(tx *Tx) error {
		if _, execErr := tx.Exec(ctx, "insert into "+table+" (id) values (2)"); execErr != nil {
			return execErr
		}

		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("rolled-back Tx error = %v, want %v", err, errRollback)
	}

	requireIntegrationCount(
		t,
		ctx,
		db,
		table,
		1,
	)
}

func TestFxLifecycleIntegration(t *testing.T) {
	var connection *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeIntegrationConfig(t, 5*time.Second)),
		MakeModule(),
		fx.Invoke(func(lc fx.Lifecycle, got *DB) {
			connection = got
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					return got.Ping(ctx)
				},
			})
		}),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build Fx app: %v", err)
	}

	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startCancel()

	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start Fx app: %v", err)
	}

	if err := connection.Ping(startCtx); err != nil {
		t.Fatalf("ping after start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()

	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop Fx app: %v", err)
	}

	if err := connection.Ping(context.Background()); err == nil {
		t.Fatal("pool remained open")
	}

}

func TestFxLifecycleStartFailsWhenPingFails(t *testing.T) {
	cfg := Config{
		Host:     "127.0.0.1:1", // unreachable
		Database: "app",
		TLS:      TLSConfig{Mode: "disable"},
		Timeouts: TimeoutConfig{Connect: 200 * time.Millisecond},
	}

	var connection *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		MakeModule(),
		fx.Invoke(func(got *DB) { connection = got }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build Fx app: %v", err)
	}

	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	startCtx, startCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer startCancel()

	if err := app.Start(startCtx); err == nil {
		t.Fatal("Fx app started with an unreachable PostgreSQL endpoint")
	}

	if connection.Pool != nil {
		t.Fatal("pool published before successful start")
	}
}

func requireIntegrationCount(
	t *testing.T,
	ctx context.Context,
	db *DB,
	table string,
	want int64,
) {
	t.Helper()

	got, err := db.FetchValue[int64](ctx, "select count(*) from "+table)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}

	if got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
}

func makeIntegrationConfig(t *testing.T, connect time.Duration) Config {
	t.Helper()

	host := os.Getenv("PGFX_TEST_HOST")
	if len(host) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	return Config{
		Host:     host,
		Database: testEnv("PGFX_TEST_DATABASE", "pgfx_test"),
		User:     testEnv("PGFX_TEST_USER", "postgres"),
		Password: secret.New(os.Getenv("PGFX_TEST_PASSWORD")),
		TLS:      TLSConfig{Mode: "disable"},
		Timeouts: TimeoutConfig{Connect: connect},
	}
}

func testEnv(name, fallback string) string {
	if value := os.Getenv(name); len(value) != 0 {
		return value
	}

	return fallback
}

// The consumer receives the same DB before startup and stops before the pool closes.
func TestFxDBConsumerLifecycle(t *testing.T) {
	var db *DB
	var isStopped bool
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeIntegrationConfig(t, 5*time.Second)),
		MakeModule(),
		fx.Invoke(func(lc fx.Lifecycle, got *DB) {
			db = got
			if db.Pool != nil {
				t.Fatal("pool allocated during construction")
			}
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					value, err := db.FetchValue[int](ctx, "SELECT 1")
					if err == nil && value != 1 {
						return errors.New("unexpected value")
					}
					return err
				},
				OnStop: func(ctx context.Context) error { isStopped = true; return db.Ping(ctx) },
			})
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if err := app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !isStopped {
		t.Fatal("consumer stop hook not called")
	}
	if err := db.Ping(t.Context()); err == nil {
		t.Fatal("pool not closed")
	}
}

// A later hook failure closes the already-started pool during startup rollback.
func TestFxDBLaterStartFailureClosesPool(t *testing.T) {
	var db *DB
	errExpected := errors.New("consumer start failed")
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeIntegrationConfig(t, 5*time.Second)),
		MakeModule(),
		fx.Invoke(func(lc fx.Lifecycle, got *DB) {
			db = got
			lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
				if err := db.Ping(ctx); err != nil {
					return err
				}
				return errExpected
			}})
		}),
	)
	if err := app.Start(t.Context()); !errors.Is(err, errExpected) {
		t.Fatalf("start: %v", err)
	}
	if db.Pool == nil {
		t.Fatal("database hook did not start")
	}
	if err := db.Ping(t.Context()); err == nil {
		t.Fatal("pool not closed after rollback")
	}
}
