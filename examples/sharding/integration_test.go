//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
	pgfxtest "github.com/uchaloop/pgfx/test"
	"go.uber.org/fx"
)

func TestItemTransaction(t *testing.T) {
	router, _ := makeFixture(t)
	ctx := t.Context()

	for _, c := range []item{{ID: 42, Version: 2, Payload: "current"}, {ID: 42, Version: 1, Payload: "old"}, {ID: 42, Version: 2, Payload: "current"}} {
		if _, err := writeItem(ctx, router, c); err != nil {
			t.Fatal(err)
		}
	}

	assertItem(
		t,
		router,
		"a",
		item{ID: 42, Version: 2, Payload: "current"},
	)

	db := router.Shards()[0].Master
	count, err := db.FetchValue[int](ctx, "SELECT count(*) FROM item_history")
	if err != nil || count != 1 {
		t.Fatalf("history after duplicate and stale versions: %d, %v", count, err)
	}

	// Force the second statement to fail; the item update must roll back too.
	if _, err := db.Exec(ctx, "ALTER TABLE item_history ADD CONSTRAINT reject_version CHECK (version < 3)"); err != nil {
		t.Fatal(err)
	}

	if _, err := writeItem(ctx, router, item{ID: 42, Version: 3, Payload: "rollback"}); !pgfx.IsSQLState(err, "23514") {
		t.Fatalf("history failure: %v", err)
	}

	const query = `
		SELECT
			id,
			version,
			payload
		FROM items
		WHERE id = 42
	`

	got, err := db.FetchRow[item](ctx, query)
	if err != nil || got != (item{ID: 42, Version: 2, Payload: "current"}) {
		t.Fatalf("rollback: %+v, %v", got, err)
	}
}

func TestItemMigration(t *testing.T) {
	router, _ := makeFixture(t)
	if err := router.Update(makeMigrationLayout(t)); err != nil {
		t.Fatal(err)
	}

	c := item{ID: 42, Version: 1, Payload: "both"}
	if _, err := writeItem(t.Context(), router, c); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a", "b"} {
		assertItem(
			t,
			router,
			name,
			c,
		)
	}

	// A failure on the source must not skip the target's independent transaction.
	if _, err := router.Shards()[0].Master.Exec(t.Context(), "ALTER TABLE item_history ADD CONSTRAINT reject_version CHECK (version < 2)"); err != nil {
		t.Fatal(err)
	}

	c.Version = 2
	results, err := writeItem(t.Context(), router, c)
	if err == nil || len(results) != 2 || results[0].Err == nil || results[1].Err != nil {
		t.Fatalf("migration: %+v, %v", results, err)
	}

	assertItem(
		t,
		router,
		"b",
		c,
	)
	const query = `
		SELECT
			count(*)
		FROM item_history
		WHERE id = 42
	`

	count, err := router.Shards()[1].Master.FetchValue[int](t.Context(), query)
	if err != nil || count != 2 {
		t.Fatalf("accepted versions must retain history: %d, %v", count, err)
	}

	c.Version = 1
	assertItem(
		t,
		router,
		"a",
		c,
	)
}

func TestPartitionPartialSuccess(t *testing.T) {
	router, _ := makeFixture(t)
	if err := router.Update(makeMigrationLayout(t)); err != nil {
		t.Fatal(err)
	}

	dbs := router.Shards()
	// A constraint failure on b rolls back its entire group, not a's group.
	if _, err := dbs[1].Master.Exec(t.Context(), "ALTER TABLE item_history ADD CONSTRAINT reject_version CHECK (version < 2)"); err != nil {
		t.Fatal(err)
	}

	items := []item{
		{ID: 43, Version: 1, Payload: "other"},
		{ID: 600, Version: 1, Payload: "b only"},
		{ID: 42, Version: 1, Payload: "old"},
		{ID: 42, Version: 2, Payload: "current"},
	}

	results, err := writeBatch(t.Context(), router, items)
	if err == nil || len(results) != len(items) {
		t.Fatalf("partial results: %+v, %v", results, err)
	}

	for i, result := range results {
		want := []string{"a"}
		if i == 1 {
			want = nil
		}

		if !slices.Equal(result.Succeeded, want) || !slices.Equal(result.Failed, []string{"b"}) {
			t.Fatalf("index %d: %+v", i, result)
		}
	}

	assertItem(
		t,
		router,
		"a",
		items[3],
	)
	assertItem(
		t,
		router,
		"a",
		items[0],
	)
	for _, query := range []string{"SELECT count(*) FROM items", "SELECT count(*) FROM item_history"} {
		count, err := dbs[1].Master.FetchValue[int](t.Context(), query)
		if err != nil || count != 0 {
			t.Fatalf("failed group retained data: %d, %v", count, err)
		}
	}

	if _, err := dbs[1].Master.Exec(t.Context(), "ALTER TABLE item_history DROP CONSTRAINT reject_version"); err != nil {
		t.Fatal(err)
	}

	results, err = writeBatch(t.Context(), router, items)
	if err != nil {
		t.Fatal(err)
	}

	for i, result := range results {
		want := []string{"a", "b"}
		if i == 1 {
			want = []string{"b"}
		}

		if len(result.Failed) != 0 || !slices.Equal(result.Succeeded, want) {
			t.Fatalf("retry index %d: %+v", i, result)
		}
	}

	for _, name := range []string{"a", "b"} {
		assertItem(
			t,
			router,
			name,
			items[3],
		)
		assertItem(
			t,
			router,
			name,
			items[0],
		)
	}

	assertItem(
		t,
		router,
		"b",
		items[1],
	)
}

func TestFxShards(t *testing.T) {
	router, app := makeFixture(t)
	for _, c := range []item{{ID: 42, Version: 1, Payload: "a"}, {ID: 600, Version: 1, Payload: "b"}} {
		if _, err := writeItem(t.Context(), router, c); err != nil {
			t.Fatal(err)
		}
	}

	assertItem(
		t,
		router,
		"a",
		item{ID: 42, Version: 1, Payload: "a"},
	)
	assertItem(
		t,
		router,
		"b",
		item{ID: 600, Version: 1, Payload: "b"},
	)
	if err := app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, s := range router.Shards() {
		if err := s.Master.Ping(t.Context()); err == nil {
			t.Fatal("pool survived Fx stop")
		}
	}

	cfgA, cfgB := testConfigs(t)
	cfgB.Host = ""
	invalid := makeApp(
		cfgA,
		cfgB,
		makeTestLayoutConfig(t),
		&router,
	)
	if invalid.Err() == nil {
		t.Fatal("invalid configuration accepted")
	}
}

func TestShardDockerRecovery(t *testing.T) {
	container := os.Getenv("PGFX_SHARD_DOCKER_CONTAINER")
	if len(container) == 0 {
		t.Skip("PGFX_SHARD_DOCKER_CONTAINER is not set")
	}

	router, _ := makeFixture(t)
	if err := router.Update(makeMigrationLayout(t)); err != nil {
		t.Fatal(err)
	}

	items := []item{{ID: 42, Version: 1, Payload: "current"}}
	if _, err := writeBatch(t.Context(), router, items); err != nil {
		t.Fatal(err)
	}

	// Register recovery before stopping, so a failed assertion does not leave b down.
	t.Cleanup(func() {
		runDocker(t, "start", container)
		awaitPostgres(t, router.Shards()[1].Master)
	})
	runDocker(
		t,
		"stop",
		"--time",
		"1",
		container,
	)
	cfgA, cfgB := testConfigs(t)
	var other *shard.Router[int64]
	lazy := makeApp(
		cfgA,
		cfgB,
		makeTestLayoutConfig(t),
		&other,
	)
	startApp(t, lazy)
	if _, err := other.Write(t.Context(), 42, func(ctx context.Context, db *pgfx.DB) error { return db.Ping(ctx) }); err != nil {
		t.Fatal(err)
	}

	strict := makeApp(
		cfgA,
		cfgB,
		makeTestLayoutConfig(t),
		&other,
		pgfx.WithStartupPing(),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := strict.Start(ctx); err == nil {
		_ = strict.Stop(ctx)
		t.Fatal("startup ping accepted stopped shard")
	}

	items[0].Version = 2
	results, err := writeBatch(t.Context(), router, items)
	if err == nil || !slices.Equal(results[0].Failed, []string{"b"}) || !slices.Equal(results[0].Succeeded, []string{"a"}) {
		t.Fatalf("outage: %+v, %v", results, err)
	}

	_, err = writeBatch(t.Context(), router, items)
	if !errors.Is(err, pgfx.ErrBreakerOpen) {
		t.Fatalf("breaker: %v", err)
	}

	assertItem(
		t,
		router,
		"a",
		items[0],
	)
	runDocker(t, "start", container)
	deadline, stop := context.WithTimeout(t.Context(), 15*time.Second)
	defer stop()
	// Readiness bypasses the breaker; retries below exercise the real probe.
	for {
		if err := router.Shards()[1].Master.Ping(deadline); err == nil {
			break
		}

		wait(t, deadline)
	}

	for {
		results, err = writeBatch(deadline, router, items)
		if err == nil {
			break
		}

		if !errors.Is(err, pgfx.ErrBreakerOpen) {
			t.Fatal(err)
		}

		wait(t, deadline)
	}

	if len(results[0].Succeeded) != 2 || len(results[0].Failed) != 0 {
		t.Fatalf("recovery: %+v", results)
	}

	assertItem(
		t,
		router,
		"b",
		items[0],
	)
}

func makeFixture(t *testing.T) (*shard.Router[int64], *fx.App) {
	t.Helper()

	cfgA, cfgB := testConfigs(t)
	schema := fmt.Sprintf("shard_example_%d", time.Now().UnixNano())
	for _, cfg := range []pgfx.Config{cfgA, cfgB} {
		db, err := pgfx.MakeDB(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(db.Close)
		if _, err := db.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
				t.Error(err)
			}

		})
	}

	var router *shard.Router[int64]
	app := makeApp(
		cfgA,
		cfgB,
		makeTestLayoutConfig(t),
		&router,
		pgfx.WithRuntimeParam("search_path", schema),
	)
	startApp(t, app)
	for _, s := range router.Shards() {
		if _, err := s.Master.Exec(t.Context(), schemaSQL); err != nil {
			t.Fatal(err)
		}
	}

	return router, app
}

func testConfigs(t *testing.T) (pgfx.Config, pgfx.Config) {
	t.Helper()

	if len(os.Getenv("PGFX_TEST_HOST")) == 0 || len(os.Getenv("PGFX_TEST_HOST_B")) == 0 {
		t.Skip("two PostgreSQL hosts are required")
	}

	cfg, err := pgfxtest.Config()
	if err != nil {
		t.Fatal(err)
	}

	cfg.Pool.MaxConns = 1
	cfg.Timeouts.Connect = time.Second
	cfg.Timeouts.Statement = 2 * time.Second
	cfg.Breaker = pgfx.BreakerConfig{Failures: 1, OpenFor: 3 * time.Second}
	other := cfg
	other.Host = os.Getenv("PGFX_TEST_HOST_B")
	return cfg, other
}

func startApp(t *testing.T, app *fx.App) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}

	})
}

func makeMigrationLayout(t *testing.T) *shard.Layout {
	t.Helper()

	cfg := makeTestLayoutConfig(t)
	cfg.Version = 2
	cfg.Ranges[0].Target = "b"
	result, err := shard.MakeLayout(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func assertItem(t *testing.T, router *shard.Router[int64], name string, want item) {
	t.Helper()

	for _, s := range router.Shards() {
		if s.Name != name {
			continue
		}

		const query = `
			SELECT
				id,
				version,
				payload
			FROM items
			WHERE id = $1
		`

		got, err := s.Master.FetchRow[item](t.Context(), query, want.ID)
		if err != nil || got != want {
			t.Fatalf(
				"%s item: %+v, %v",
				name,
				got,
				err,
			)
		}

		const historyQuery = `
			SELECT
				max(version)
			FROM item_history
			WHERE id = $1
		`

		version, err := s.Master.FetchValue[int64](t.Context(), historyQuery, want.ID)
		if err != nil || version != want.Version {
			t.Fatalf(
				"%s history: %d, %v",
				name,
				version,
				err,
			)
		}

		return
	}

	t.Fatalf("missing shard %s", name)
}

func runDocker(t *testing.T, args ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker: %v: %s", err, output)
	}
}

func wait(t *testing.T, ctx context.Context) {
	t.Helper()

	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(50 * time.Millisecond):
	}
}

func makeTestLayoutConfig(t *testing.T) shard.LayoutConfig {
	t.Helper()

	cfg, err := makeLayoutConfig()
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func awaitPostgres(t *testing.T, db *pgfx.DB) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if err := db.Ping(ctx); err == nil {
			return
		}

		wait(t, ctx)
	}
}
