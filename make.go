package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MakeDB validates cfg and creates a pool, applying the runtime opts. It does
// not wait for the server unless WithStartupPing is set (then it pings and
// fails); call Ping otherwise. A pool with MinConns may still dial in the
// background. The caller owns the returned DB and must
// call Close; MakeModule manages verification and cleanup through the Fx lifecycle.
// Health is validated but no health monitor is started by MakeDB.
func MakeDB(ctx context.Context, cfg Config, opts ...Option) (*DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	resolvedOpts := makeOptions(opts...)
	state := &connState{name: resolvedOpts.name, breaker: makeBreaker(cfg.Breaker)}
	resolvedOpts.state = state

	poolCfg, err := cfg.poolConfig(resolvedOpts)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	if resolvedOpts.isStartupPing {
		if err := pool.Ping(ctx); err != nil {
			pool.Close()

			return nil, err
		}
	}

	return &DB{Pool: pool, state: state}, nil
}
