package pgfx

import (
	"context"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// healthHookError preserves the cause while identifying application hook failures
// inside pgconn.ConnectError. Its message and errors.Is/errors.As behavior stay intact.
type healthHookError struct{ err error }

func (e *healthHookError) Error() string { return e.err.Error() }

func (e *healthHookError) Unwrap() error { return e.err }

func wrapHealthHookError(err error) error {
	if err == nil {
		return nil
	}

	// pgx checks this concrete type to implement prefer-standby fallback.
	if _, isOK := err.(*pgconn.NotPreferredError); isOK {
		return err
	}

	return &healthHookError{err: err}
}

func wrapHealthConnectHooks(cfg *pgxpool.Config) {
	before := cfg.BeforeConnect
	cfg.BeforeConnect = func(ctx context.Context, connCfg *pgx.ConnConfig) error {
		if before != nil {
			if err := before(ctx, connCfg); err != nil {
				return err
			}
		}

		connCfg.Config.AfterConnect = wrapHealthConnectHook(connCfg.Config.AfterConnect)
		connCfg.ValidateConnect = wrapHealthConnectHook(connCfg.ValidateConnect)

		if hook := connCfg.AfterNetConnect; hook != nil {
			connCfg.AfterNetConnect = func(ctx context.Context, cfg *pgconn.Config, conn net.Conn) (net.Conn, error) {
				result, err := hook(ctx, cfg, conn)
				if err != nil {
					// pgx closes the returned connection when AfterNetConnect fails.
					return result, wrapHealthHookError(err)
				}

				return result, nil
			}
		}

		return nil
	}
}

func wrapHealthConnectHook(hook func(context.Context, *pgconn.PgConn) error) func(context.Context, *pgconn.PgConn) error {
	if hook == nil {
		return nil
	}

	return func(ctx context.Context, conn *pgconn.PgConn) error {
		return wrapHealthHookError(hook(ctx, conn))
	}
}
