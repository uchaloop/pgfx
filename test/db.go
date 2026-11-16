// Package pgfxtest connects integration tests to an existing PostgreSQL server.
// It does not create databases, isolate tests, run migrations or skip tests.
// Import it as pgfxtest "github.com/uchaloop/pgfx/test".
package pgfxtest

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/secret/v2"
)

// MakeDB reads PGFX_TEST_HOST and PGFX_TEST_DATABASE (both required), plus
// PGFX_TEST_USER, PGFX_TEST_PASSWORD and PGFX_TEST_TLS_MODE (default "disable").
// PGFX_TEST_MAX_CONNS accepts a nonnegative int32; empty or zero keeps the
// pgxpool default. Set it to 1 for tests that require one pooled connection.
// Invalid values return an error before creating the pool.
// Disabled TLS is intended only for a disposable local test database; set a
// suitable TLS mode or pass pgfx.WithTLS for other environments.
// POSTGRES_* variables are not read. Other libpq environment/default behavior
// remains: empty credentials may use PGUSER, PGPASSWORD or .pgpass. Set test
// credentials explicitly when that fallback is not desired.
//
// It applies opts, then verifies connectivity with Ping using ctx. Supply a
// deadline; no timeout is added. On failure the pool is closed and nil returned.
// The caller owns a successful DB and must close it (for example, t.Cleanup).
// Options have the same semantics as pgfx.MakeDB; tracing is off by default.
func MakeDB(ctx context.Context, opts ...pgfx.Option) (*pgfx.DB, error) {
	mode := os.Getenv("PGFX_TEST_TLS_MODE")
	if len(mode) == 0 {
		mode = "disable"
	}

	cfg := pgfx.Config{
		Host:     os.Getenv("PGFX_TEST_HOST"),
		Database: os.Getenv("PGFX_TEST_DATABASE"),
		User:     os.Getenv("PGFX_TEST_USER"),
		Password: secret.New(os.Getenv("PGFX_TEST_PASSWORD")),
		TLS:      pgfx.TLSConfig{Mode: mode},
	}

	if value := os.Getenv("PGFX_TEST_MAX_CONNS"); len(value) != 0 {
		maxConns, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid PGFX_TEST_MAX_CONNS: %w", err)
		}

		if maxConns < 0 {
			return nil, fmt.Errorf("PGFX_TEST_MAX_CONNS must not be negative")
		}

		cfg.Pool.MaxConns = int32(maxConns)
	}

	db, err := pgfx.MakeDB(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(ctx); err != nil {
		db.Close()

		return nil, err
	}

	return db, nil
}
