package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/secret/v2"
	"go.uber.org/fx"
)

func main() {
	cfg := pgfx.Config{
		Host:     os.Getenv("PGFX_TEST_HOST"),
		Database: os.Getenv("PGFX_TEST_DATABASE"),
		User:     os.Getenv("PGFX_TEST_USER"),
		Password: secret.New(os.Getenv("PGFX_TEST_PASSWORD")),
		Health: pgfx.HealthConfig{
			IsEnabled:    true,
			Interval:     10 * time.Second,
			Timeout:      3 * time.Second,
			FailureAfter: time.Minute,
		},
		TLS: pgfx.TLSConfig{Mode: "disable"}, // Local disposable database only.
	}
	fx.New(
		pgfx.HealthModule,
		fx.Supply(cfg),
		pgfx.MakeModule(pgfx.WithHealthSQLStates(pgfx.DefaultHealthSQLStates()...)),
		fx.Invoke(registerQuery),
	).Run()
}

func registerQuery(lc fx.Lifecycle, connection *pgfx.Connection) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			db, err := connection.DB()
			if err != nil {
				return err
			}

			value, err := db.FetchValue[int](ctx, "SELECT 1")
			if err != nil {
				return err
			}

			fmt.Println(value)

			return nil
		},
	})
}
