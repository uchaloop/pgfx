package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
	pgfxtest "github.com/uchaloop/pgfx/test"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	cfg, err := pgfxtest.Config()
	if err != nil {
		return err
	}

	if len(os.Getenv("PGFX_TEST_HOST_B")) == 0 {
		return errors.New("PGFX_TEST_HOST_B is required")
	}

	cfg.Pool.MaxConns = 2
	cfg.Timeouts.Connect = time.Second
	cfg.Breaker = pgfx.BreakerConfig{Failures: 2, OpenFor: time.Second}
	cfgB := cfg
	cfgB.Host = os.Getenv("PGFX_TEST_HOST_B")

	layout, err := makeLayoutConfig()
	if err != nil {
		return err
	}

	var router *shard.Router[int64]
	app := makeApp(
		cfg,
		cfgB,
		layout,
		&router,
		pgfx.WithRuntimeParam("search_path", "pgfx_shard_example"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		return err
	}

	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err = errors.Join(err, app.Stop(stopCtx))
	}()

	for _, s := range router.Shards() {
		if _, err := s.Master.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS pgfx_shard_example"); err != nil {
			return err
		}

		if _, err := s.Master.Exec(ctx, schemaSQL); err != nil {
			return err
		}
	}

	if _, err := writeItem(ctx, router, item{ID: 42, Version: 1, Payload: "single"}); err != nil {
		return err
	}

	results, err := writeBatch(ctx, router, []item{{ID: 42, Version: 2, Payload: "latest"}, {ID: 600, Version: 1, Payload: "other shard"}})
	if err != nil {
		return err
	}

	for i, result := range results {
		fmt.Printf(
			"item %d: succeeded=%v failed=%v\n",
			i,
			result.Succeeded,
			result.Failed,
		)
	}

	return nil
}
