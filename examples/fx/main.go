package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/secret/v2"
	"go.uber.org/fx"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg := pgfx.Config{
		Host:     os.Getenv("PGFX_TEST_HOST"),
		Database: os.Getenv("PGFX_TEST_DATABASE"),
		User:     os.Getenv("PGFX_TEST_USER"),
		Password: secret.New(os.Getenv("PGFX_TEST_PASSWORD")),
		TLS:      pgfx.TLSConfig{Mode: "disable"}, // Local disposable database only.
	}
	app := fx.New(fx.Supply(cfg), pgfx.MakeModule(), fx.Invoke(registerQuery))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		return err
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	return app.Stop(stopCtx)
}

func registerQuery(lc fx.Lifecycle, db *pgfx.DB) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			value, err := db.FetchValue[int](ctx, "SELECT 1")
			if err != nil {
				return err
			}

			fmt.Println(value)

			return nil
		},
	})
}
