package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Make validates cfg and opens a connection for it, applying the runtime opts.
// The caller owns the returned DB and must call Close; the Fx module does that
// through the lifecycle.
func Make(ctx context.Context, cfg Config, opts ...Option) (*DB, error) {
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

	return &DB{Pool: pool}, nil
}
