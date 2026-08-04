//go:build integration

package pgfx

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/secret"
	"go.uber.org/fx"
)

func TestPostgresIntegration(t *testing.T) {
	host := os.Getenv("PGFX_TEST_HOST")
	if host == "" {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	cfg := integrationConfig(host, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := Make(ctx, cfg)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	one, err := FetchValue[int](ctx, pool, "select 1")
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

	if err := Tx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "insert into "+table+" (id) values (1)")

		return err
	}); err != nil {
		t.Fatalf("committed Tx: %v", err)
	}
	requireIntegrationCount(t, ctx, pool, table, 1)

	wantRollback := errors.New("rollback requested")
	err = Tx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, execErr := tx.Exec(ctx, "insert into "+table+" (id) values (2)"); execErr != nil {
			return execErr
		}

		return wantRollback
	})
	if !errors.Is(err, wantRollback) {
		t.Fatalf("rolled-back Tx error = %v, want %v", err, wantRollback)
	}
	requireIntegrationCount(t, ctx, pool, table, 1)
}

func TestFxLifecycleIntegration(t *testing.T) {
	host := os.Getenv("PGFX_TEST_HOST")
	if host == "" {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	var pool *pgxpool.Pool
	app := fx.New(
		fx.NopLogger,
		fx.Supply(integrationConfig(host, 5*time.Second)),
		Module(),
		fx.Invoke(func(got *pgxpool.Pool) { pool = got }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build Fx app: %v", err)
	}
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
	})

	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startCancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start Fx app: %v", err)
	}
	if err := pool.Ping(startCtx); err != nil {
		t.Fatalf("ping after start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop Fx app: %v", err)
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

	var pool *pgxpool.Pool
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		Module(),
		fx.Invoke(func(got *pgxpool.Pool) { pool = got }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build Fx app: %v", err)
	}
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
	})

	startCtx, startCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer startCancel()
	if err := app.Start(startCtx); err == nil {
		t.Fatal("Fx app started with an unreachable PostgreSQL endpoint")
	}
}

func requireIntegrationCount(
	t *testing.T,
	ctx context.Context,
	q Querier,
	table string,
	want int64,
) {
	t.Helper()

	got, err := FetchValue[int64](ctx, q, "select count(*) from "+table)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
}

func integrationConfig(host string, connect time.Duration) Config {
	return Config{
		Host:     host,
		Database: testEnv("PGFX_TEST_DATABASE", "pgfx_test"),
		User:     testEnv("PGFX_TEST_USER", "postgres"),
		Password: secret.Secret(os.Getenv("PGFX_TEST_PASSWORD")),
		TLS:      TLSConfig{Mode: "disable"},
		Timeouts: TimeoutConfig{Connect: connect},
	}
}

func testEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}
