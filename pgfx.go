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
