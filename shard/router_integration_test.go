//go:build integration

package shard_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
	pgfxtest "github.com/uchaloop/pgfx/test"
	"go.uber.org/fx"
)

func integrationConfig(t *testing.T) pgfx.Config {
	t.Helper()

	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	cfg, err := pgfxtest.Config()
	if err != nil {
		t.Fatal(err)
	}

	cfg.Breaker = pgfx.BreakerConfig{Failures: 2, OpenFor: time.Minute}
	cfg.Timeouts.Connect = time.Second

	return cfg
}

// makeSchemaShards creates schemas shard_a and shard_b with an items table and
// returns connections a and b whose search_path points at them.
func makeSchemaShards(t *testing.T) map[string]*pgfx.DB {
	t.Helper()

	cfg := integrationConfig(t)

	admin, err := pgfx.MakeDB(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(admin.Close)

	for _, schema := range []string{"shard_a", "shard_b"} {
		if _, err := admin.Exec(t.Context(), `
			DROP SCHEMA IF EXISTS `+schema+` CASCADE;
			CREATE SCHEMA `+schema+`;
			CREATE TABLE `+schema+`.items (
				id bigint PRIMARY KEY,
				v int NOT NULL,
				version bigint NOT NULL
			)
		`); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS shard_a CASCADE; DROP SCHEMA IF EXISTS shard_b CASCADE")
	})

	dbs := make(map[string]*pgfx.DB, 2)

	for _, name := range []string{"a", "b"} {
		db, err := pgfx.MakeDB(t.Context(), cfg, pgfx.WithName(name), pgfx.WithRuntimeParam("search_path", "shard_"+name))
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(db.Close)
		dbs[name] = db
	}

	return dbs
}

const upsertItem = `
	INSERT INTO items (
		id,
		v,
		version
	)
	VALUES (
		$1,
		$2,
		$3
	)
	ON CONFLICT (id) DO UPDATE
	SET
		v = excluded.v,
		version = excluded.version
	WHERE items.version < excluded.version
`

func versionOf(t *testing.T, db *pgfx.DB, id int64) int64 {
	t.Helper()

	version, isFound, err := db.FetchOptionalRow[struct {
		Version int64 `db:"version"`
	}](t.Context(), "SELECT version FROM items WHERE id = $1", id)
	if err != nil {
		t.Fatal(err)
	}

	if !isFound {
		return 0
	}

	return version.Version
}

func TestRouterIntegration(t *testing.T) {
	dbs := makeSchemaShards(t)
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, updateNodes, "a:0-1;b:2-3"))

	write := func(id int64) error {
		_, err := router.Write(t.Context(), id, func(ctx context.Context, db *pgfx.DB) error {
			_, err := db.Exec(ctx, upsertItem, id, 1, 1)

			return err
		})

		return err
	}

	if err := router.Update(makeLayout(t, 2, 4, updateNodes, "a:0-1;b>a:2-3")); err != nil {
		t.Fatal(err)
	}

	if err := write(2); err != nil {
		t.Fatal(err)
	}

	if versionOf(t, dbs["a"], 2) != 1 || versionOf(t, dbs["b"], 2) != 1 {
		t.Fatal("a migrating write did not reach both shards")
	}

	name, err := router.Read(t.Context(), 2, func(_ context.Context, db *pgfx.DB) (string, error) { return db.Name(), nil })
	if err != nil || name != "b" {
		t.Fatalf("read from %q, err %v", name, err)
	}

	if err := router.Update(makeLayout(t, 3, 4, updateNodes, "a:0-3")); err != nil {
		t.Fatal(err)
	}

	if err := write(3); err != nil {
		t.Fatal(err)
	}

	if versionOf(t, dbs["a"], 3) != 1 || versionOf(t, dbs["b"], 3) != 0 {
		t.Fatal("an active write touched the old shard")
	}

	counts := make(map[string]int64)

	err = router.Each(t.Context(), 1, func(ctx context.Context, s shard.Shard) error {
		count, err := s.Master.FetchValue[int64](ctx, "SELECT count(*) FROM items")
		counts[s.Name] = count

		return err
	})
	// b still holds the row written while migrating: cleaning up a moved bucket
	// is the application's step.
	if err != nil || counts["a"] != 2 || counts["b"] != 1 {
		t.Fatalf("counts %v, err %v", counts, err)
	}
}

func TestWriteConvergesWithVersion(t *testing.T) {
	dbs := makeSchemaShards(t)
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, updateNodes, "a>b:0-3"))

	const id = 1

	firstPaused, secondDone := make(chan struct{}), make(chan struct{})
	errs := make(chan error, 2)

	write := func(version int64, beforeTarget func()) {
		_, err := router.Write(t.Context(), id, func(ctx context.Context, db *pgfx.DB) error {
			if db == dbs["b"] && beforeTarget != nil {
				beforeTarget()
			}

			_, err := db.Exec(ctx, upsertItem, id, version, version)

			return err
		})
		errs <- err
	}

	// Version 1 reaches a, then waits; version 2 reaches a and b; then version 1
	// reaches b. So a sees 1 then 2, and b sees 2 then 1.
	go write(1, func() {
		close(firstPaused)
		<-secondDone
	})

	<-firstPaused
	write(2, nil)
	close(secondDone)

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if versionOf(t, dbs["a"], id) != 2 || versionOf(t, dbs["b"], id) != 2 {
		t.Fatalf("shards diverged: a=%d b=%d", versionOf(t, dbs["a"], id), versionOf(t, dbs["b"], id))
	}
}

func TestRouterTwoInstancesIntegration(t *testing.T) {
	cfgA := integrationConfig(t)

	hostB := os.Getenv("PGFX_TEST_HOST_B")
	if len(hostB) == 0 {
		t.Skip("PGFX_TEST_HOST_B is not set")
	}

	cfgB, cfgC := cfgA, cfgA
	cfgB.Host = hostB
	cfgC.Host = "127.0.0.1:55435"

	var router *shard.Router[int64]

	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			fx.Annotate(cfgA, fx.ResultTags(`name:"a"`)),
			fx.Annotate(cfgB, fx.ResultTags(`name:"b"`)),
			fx.Annotate(cfgC, fx.ResultTags(`name:"c"`)),
		),
		pgfx.MakeModuleFor("a"),
		pgfx.MakeModuleFor("b"),
		pgfx.MakeModuleFor("c"),
		fx.Invoke(fx.Annotate(
			func(dbs []*pgfx.DB) error {
				layout, err := shard.MakeLayout(shard.LayoutConfig{
					Version: 1,
					Buckets: 3,
					Nodes:   parseNodes(t, "a:a;b:b;c:c"),
					Ranges:  parseRanges(t, "a:0;b:1;c:2"),
				})
				if err != nil {
					return err
				}

				router, err = shard.MakeRouter(shard.ModuloBuckets[int64](3), layout, dbs)

				return err
			},
			fx.ParamTags(`group:"pgfx"`),
		)),
	)

	startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start with shard c down: %v", err)
	}

	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	ping := func(ctx context.Context, db *pgfx.DB) error {
		_, err := db.Exec(ctx, "SELECT 1")

		return err
	}

	for _, key := range []int64{0, 1} {
		if _, err := router.Write(t.Context(), key, ping); err != nil {
			t.Fatalf("key %d: %v", key, err)
		}
	}

	for range 2 {
		if _, err := router.Write(t.Context(), 2, ping); err == nil {
			t.Fatal("a write to the down shard succeeded")
		}
	}

	route, err := router.Route(2)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := route.Writes[0].Attempt(t.Context()); !errors.Is(err, shard.ErrShardUnavailable) {
		t.Fatalf("Attempt on the down shard = %v", err)
	}

	err = router.Each(t.Context(), 2, func(ctx context.Context, s shard.Shard) error {
		ctx, attempt, err := shard.Target{Shard: s.Name, DB: s.Master}.Attempt(ctx)
		if err != nil {
			return err
		}

		err = ping(ctx, s.Master)
		attempt.Done(err)

		return err
	})
	if !errors.Is(err, shard.ErrShardUnavailable) || !strings.Contains(err.Error(), "shard c") ||
		strings.Contains(err.Error(), "shard a") || strings.Contains(err.Error(), "shard b") {
		t.Fatalf("Each = %v", err)
	}
}
