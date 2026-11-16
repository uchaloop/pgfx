//go:build integration

package pgfx

import (
	"context"
	"errors"
	"os"
	"sync"
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

	wantRollback := errors.New("rollback requested")
	err = db.Transaction(ctx, pgx.TxOptions{}, func(tx *Tx) error {
		if _, execErr := tx.Exec(ctx, "insert into "+table+" (id) values (2)"); execErr != nil {
			return execErr
		}

		return wantRollback
	})
	if !errors.Is(err, wantRollback) {
		t.Fatalf("rolled-back Tx error = %v, want %v", err, wantRollback)
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
	var connection *Connection
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeIntegrationConfig(t, 5*time.Second)),
		MakeModule(),
		fx.Invoke(func(lc fx.Lifecycle, got *Connection) {
			connection = got
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					db, err := got.DB()
					if err != nil {
						return err
					}

					return db.Ping(ctx)
				},
			})
		}),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build Fx app: %v", err)
	}

	t.Cleanup(func() {
		if connection != nil {
			connection.close()
		}
	})

	done := make(chan struct{})
	var readers sync.WaitGroup

	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					db, err := connection.DB()
					if err == nil {
						if db == nil || db.Pool == nil {
							t.Error("published uninitialized database")

							return
						}

						_ = db.Config()
					} else if !errors.Is(err, ErrConnectionNotReady) && !errors.Is(err, ErrConnectionClosed) {
						t.Errorf("unexpected readiness error: %v", err)

						return
					}
				}
			}
		})
	}
	defer func() { close(done); readers.Wait() }()

	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startCancel()

	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start Fx app: %v", err)
	}

	pool, err := connection.DB()
	if err != nil {
		t.Fatal(err)
	}

	if err := pool.Ping(startCtx); err != nil {
		t.Fatalf("ping after start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()

	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop Fx app: %v", err)
	}

	if _, err := connection.DB(); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("after stop: %v", err)
	}

	if err := pool.Ping(context.Background()); err == nil {
		t.Fatal("ping after stop succeeded; pool was not closed")
	}
}

func TestFxLifecycleStartFailsWhenPingFails(t *testing.T) {
	cfg := Config{
		Host:     "127.0.0.1:1", // unreachable
		Database: "app",
		TLS:      TLSConfig{Mode: "disable"},
		Timeouts: TimeoutConfig{Connect: 200 * time.Millisecond},
	}

	var connection *Connection
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		MakeModule(),
		fx.Invoke(func(got *Connection) { connection = got }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build Fx app: %v", err)
	}

	t.Cleanup(func() {
		if connection != nil {
			connection.close()
		}
	})

	startCtx, startCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer startCancel()

	if err := app.Start(startCtx); err == nil {
		t.Fatal("Fx app started with an unreachable PostgreSQL endpoint")
	}

	if db, err := connection.DB(); db != nil || !errors.Is(err, ErrConnectionNotReady) {
		t.Fatalf("failed startup: %v, %v", db, err)
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
