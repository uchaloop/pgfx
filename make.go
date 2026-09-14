package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Make validates cfg and creates a pool, applying the runtime opts.
// Call Ping to verify connectivity. The caller owns the returned DB and must
// call Close; Module manages verification and cleanup through the Fx lifecycle.
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
