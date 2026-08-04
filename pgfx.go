// Package pgfx is a thin, Fx-first layer over pgx/pgxpool for Postgres. A pgfx
// connection is a single *pgxpool.Pool built from a plain, serializable Config.
// Runtime dependencies (tracer, query metrics, pool hooks, an in-memory
// *tls.Config) are supplied through [Option] values, never through [Config].
//
// pgfx is meant to be wired with Uber Fx. [Module] is the default single
// connection and [ModuleFor] adds a replica or another shard; both consume a
// pgfx.Config the application supplies (typically via confmaker/confx) and manage
// the pool's lifecycle. pgfx does not read any config source itself, so it stays
// decoupled from confmaker. Multiple databases are simply multiple named
// connections. To build a pool without Fx, call [Make] with a Config you fill
// yourself.
package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Make validates cfg and opens a connection pool for it, applying the runtime
// opts. The caller owns the returned pool and must Close it (the fx module does
// this via lifecycle).
func Make(ctx context.Context, cfg Config, opts ...Option) (*pgxpool.Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	poolCfg, err := cfg.poolConfig(makeOptions(opts...))
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	return pool, nil
}
