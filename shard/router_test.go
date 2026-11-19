package shard_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
)

// makeDBs creates lazy, named connections with a breaker that opens on the
// first failure; nothing listens on their port.
func makeDBs(t *testing.T, breaker pgfx.BreakerConfig, names ...string) map[string]*pgfx.DB {
	t.Helper()

	dbs := make(map[string]*pgfx.DB, len(names))

	for _, name := range names {
		db, err := pgfx.MakeDB(
			context.Background(),
			pgfx.Config{
				Host:     "127.0.0.1:1",
				Database: "x",
				TLS:      pgfx.TLSConfig{Mode: "disable"},
				Breaker:  breaker,
			},
			pgfx.WithName(name),
		)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(db.Close)
		dbs[name] = db
	}

	return dbs
}

func values(dbs map[string]*pgfx.DB) []*pgfx.DB {
	list := make([]*pgfx.DB, 0, len(dbs))
	for _, db := range dbs {
		list = append(list, db)
	}

	return list
}

var defaultBreaker = pgfx.BreakerConfig{Failures: 1, OpenFor: time.Minute}

// openBreaker fails one query on db so its breaker opens.
func openBreaker(t *testing.T, db *pgfx.DB) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := db.Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("query to an unreachable server succeeded")
	}

	if _, _, err := db.Breaker().Attempt(ctx); !errors.Is(err, pgfx.ErrBreakerOpen) {
		t.Fatalf("breaker did not open: %v", err)
	}
}

func shardNames(targets []shard.Target) []string {
	names := make([]string, len(targets))
	for i, target := range targets {
		names[i] = target.Shard
	}

	return names
}

func TestRoute(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "node1", "node1-replica", "node2", "node3")
	layout := makeLayout(t, 5, 4, "node1:node1|node1-replica;node2:node2;node3:node3", "node1:0-1;node2>node3:2-3")

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), layout, values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	active, err := router.Route(5)
	if err != nil {
		t.Fatal(err)
	}

	if active.Version != 5 || active.Bucket != 1 || !slices.Equal(shardNames(active.Writes), []string{"node1"}) ||
		!slices.Equal(active.Reads, []*pgfx.DB{dbs["node1"], dbs["node1-replica"]}) {
		t.Fatalf("active route = %+v", active)
	}

	migrating, err := router.Route(2)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(shardNames(migrating.Writes), []string{"node2", "node3"}) || migrating.Writes[1].DB != dbs["node3"] ||
		!slices.Equal(migrating.Reads, []*pgfx.DB{dbs["node2"]}) {
		t.Fatalf("migrating route = %+v", migrating)
	}
}

func TestRouteBucketOutOfRange(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a")

	router, err := shard.MakeRouter(
		shard.MakeBuckets(4, func(int) int { return 7 }),
		makeLayout(t, 1, 4, "a:a", "a:0-3"),
		values(dbs),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := router.Route(1); !errors.Is(err, shard.ErrLayout) {
		t.Fatalf("Route = %v, want ErrLayout", err)
	}
}

func TestMakeRouterRejects(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	noBreaker := makeDBs(t, pgfx.BreakerConfig{}, "c")
	unnamed := makeDBs(t, defaultBreaker, "")
	layout := makeLayout(t, 1, 4, "a:a;b:b", "a:0-1;b:2-3")

	for _, tc := range []struct {
		name    string
		buckets int
		layout  *shard.Layout
		dbs     []*pgfx.DB
	}{
		{name: "count mismatch", buckets: 8, layout: layout, dbs: values(dbs)},
		{name: "missing connection", buckets: 4, layout: layout, dbs: []*pgfx.DB{dbs["a"]}},
		{name: "duplicate name", buckets: 4, layout: layout, dbs: []*pgfx.DB{dbs["a"], dbs["b"], dbs["a"]}},
		{name: "empty name", buckets: 4, layout: layout, dbs: []*pgfx.DB{dbs["a"], dbs["b"], unnamed[""]}},
		{name: "no breaker", buckets: 4, layout: makeLayout(t, 1, 4, "c:c", "c:0-3"), dbs: values(noBreaker)},
		{name: "assembled outside pgfx", buckets: 4, layout: layout, dbs: []*pgfx.DB{dbs["a"], dbs["b"], {}}},
		{name: "nil layout", buckets: 4, dbs: values(dbs)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := shard.MakeRouter(shard.ModuloBuckets[int64](tc.buckets), tc.layout, tc.dbs); err == nil {
				t.Fatal("MakeRouter accepted the input")
			}
		})
	}
}

const updateNodes = "a:a;b:b"

func TestRouterUpdate(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), makeLayout(t, 1, 4, updateNodes, "a:0-3"), values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	if err := router.Update(makeLayout(t, 2, 4, updateNodes, "b:0-3")); !errors.Is(err, shard.ErrLayout) {
		t.Fatalf("a direct move was accepted: %v", err)
	}

	if err := router.Update(makeLayout(t, 2, 4, updateNodes, "a:0-1;a>b:2-3")); err != nil {
		t.Fatal(err)
	}

	route, err := router.Route(3)
	if err != nil {
		t.Fatal(err)
	}

	if router.Version() != 2 || !slices.Equal(shardNames(route.Writes), []string{"a", "b"}) {
		t.Fatalf("version %d, route %+v", router.Version(), route)
	}
}

func TestRouterUpdateRejectsOlderVersion(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), makeLayout(t, 7, 4, updateNodes, "a:0-3"), values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	if err := router.Update(makeLayout(t, 6, 4, updateNodes, "a:0-3")); err == nil {
		t.Fatal("an older version was accepted")
	}

	if router.Version() != 7 {
		t.Fatalf("Version = %d", router.Version())
	}
}

func TestRouterUpdateConcurrent(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	v1 := makeLayout(t, 1, 4, updateNodes, "a:0-3")
	v2 := makeLayout(t, 2, 4, updateNodes, "a>b:0-3")
	v3 := makeLayout(t, 3, 4, updateNodes, "b:0-3")

	for range 200 {
		router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), v1, values(dbs))
		if err != nil {
			t.Fatal(err)
		}

		var (
			wg           sync.WaitGroup
			errV2, errV3 error
		)

		wg.Go(func() { errV2 = router.Update(v2) })
		wg.Go(func() { errV3 = router.Update(v3) })
		wg.Wait()

		if errV3 == nil && errV2 != nil {
			t.Fatalf("v3 was applied directly over v1: v2 error %v", errV2)
		}
	}
}

func TestRouterUpdateRace(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	layouts := []*shard.Layout{
		makeLayout(t, 1, 4, updateNodes, "a:0-3"),
		makeLayout(t, 2, 4, updateNodes, "a>b:0-3"),
		makeLayout(t, 3, 4, updateNodes, "b:0-3"),
	}
	want := map[int64][]string{1: {"a"}, 2: {"a", "b"}, 3: {"b"}}

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), layouts[0], values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	done := make(chan struct{})

	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}

				route, err := router.Route(2)
				if err != nil {
					t.Error(err)

					return
				}

				if !slices.Equal(shardNames(route.Writes), want[route.Version]) {
					t.Errorf("route %v does not match version %d", shardNames(route.Writes), route.Version)

					return
				}
			}
		})
	}

	for _, layout := range layouts[1:] {
		if err := router.Update(layout); err != nil {
			t.Error(err)
		}
	}

	close(done)
	wg.Wait()
}

func TestTargetAttempt(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a")

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), makeLayout(t, 1, 4, "a:a", "a:0-3"), values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	openBreaker(t, dbs["a"])

	route, err := router.Route(1)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = route.Writes[0].Attempt(context.Background())
	if !errors.Is(err, shard.ErrShardUnavailable) || !errors.Is(err, pgfx.ErrBreakerOpen) {
		t.Fatalf("Attempt = %v", err)
	}
}

func TestLayoutCopiesConfig(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "m", "r", "x")

	cfg := shard.LayoutConfig{Version: 1, Buckets: 4, Nodes: parseNodes(t, "a:m|r;b:x"), Ranges: parseRanges(t, "a:0-3")}

	layout, err := shard.MakeLayout(cfg)
	if err != nil {
		t.Fatal(err)
	}

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](4), layout, values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	cfg.Nodes[0].Replicas[0] = "x"

	route, err := router.Route(1)
	if err != nil {
		t.Fatal(err)
	}

	if len(route.Reads) != 2 || route.Reads[1] != dbs["r"] {
		t.Fatalf("changing the config after MakeLayout changed routing: reads %v", route.Reads)
	}
}
