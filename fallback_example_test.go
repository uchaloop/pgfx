package pgfx_test

import (
	"context"

	"github.com/uchaloop/pgfx"
)

func ExampleReadWithFallback() {
	// Fx supplies initialized databases before the consumer's OnStart hook.
	readNames := func(ctx context.Context, replica, master *pgfx.DB) ([]string, error) {
		return pgfx.ReadWithFallback(
			ctx,
			[]*pgfx.DB{replica, master},
			func(ctx context.Context, db *pgfx.DB) ([]string, error) {
				return db.FetchValues[string](ctx, "SELECT name FROM users ORDER BY id")
			},
		)
	}
	_ = readNames
}
