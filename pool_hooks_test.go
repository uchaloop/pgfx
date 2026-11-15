package pgfx

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPoolHooksNilOverrides(t *testing.T) {
	cfg := makeTestPoolConfig(
		t,
		WithPrepareConn(func(context.Context, *pgx.Conn) (bool, error) { return true, nil }),
		WithAfterRelease(func(*pgx.Conn) bool { return true }),
		WithBeforeClose(func(*pgx.Conn) {}),
		WithShouldPing(func(context.Context, pgxpool.ShouldPingParams) bool { return true }),
		WithPrepareConn(nil),
		WithAfterRelease(nil),
		WithBeforeClose(nil),
		WithShouldPing(nil),
	)
	if cfg.PrepareConn != nil || cfg.AfterRelease != nil || cfg.BeforeClose != nil || cfg.ShouldPing != nil {
		t.Fatal("nil options did not restore native defaults")
	}
}
