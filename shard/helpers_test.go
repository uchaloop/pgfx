package shard_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
)

var errApp = errors.New("application error")

func makeRouter(t *testing.T, dbs map[string]*pgfx.DB, layout *shard.Layout) *shard.Router[int64] {
	t.Helper()

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](layout.Buckets()), layout, values(dbs))
	if err != nil {
		t.Fatal(err)
	}

	return router
}

// halfOpen opens db's breaker and waits until it lets a probe through.
func halfOpen(t *testing.T, db *pgfx.DB, openFor time.Duration) {
	t.Helper()

	openBreaker(t, db)
	time.Sleep(openFor + 10*time.Millisecond)
}

func isProbeAvailable(t *testing.T, db *pgfx.DB) bool {
	t.Helper()

	_, attempt, err := db.Breaker().Attempt(context.Background())
	if err != nil {
		return false
	}

	attempt.Release()

	return true
}

func TestWriteActive(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "node1")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "node1:node1", "node1:0-3"))

	var got []*pgfx.DB

	results, err := router.Write(context.Background(), 1, func(_ context.Context, db *pgfx.DB) error {
		got = append(got, db)

		return nil
	})
	if err != nil || !slices.Equal(got, []*pgfx.DB{dbs["node1"]}) || !slices.Equal(results, []shard.ShardResult{{Shard: "node1"}}) {
		t.Fatalf("results %+v, calls %v, err %v", results, got, err)
	}
}

func TestWriteMigratingSourceFails(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "node2", "node3")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "node2:node2;node3:node3", "node2>node3:0-3"))

	var got []*pgfx.DB

	results, err := router.Write(context.Background(), 1, func(_ context.Context, db *pgfx.DB) error {
		got = append(got, db)

		if db == dbs["node2"] {
			return errApp
		}

		return nil
	})

	if !slices.Equal(got, []*pgfx.DB{dbs["node2"], dbs["node3"]}) {
		t.Fatalf("calls %v", got)
	}

	if len(results) != 2 || !errors.Is(results[0].Err, errApp) || results[1].Err != nil {
		t.Fatalf("results %+v", results)
	}

	if !errors.Is(err, errApp) || !strings.Contains(err.Error(), "write node2") {
		t.Fatalf("err %v", err)
	}
}

func TestWriteMigratingTargetOpen(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "node2", "node3")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "node2:node2;node3:node3", "node2>node3:0-3"))
	openBreaker(t, dbs["node3"])

	var got []*pgfx.DB

	_, err := router.Write(context.Background(), 1, func(_ context.Context, db *pgfx.DB) error {
		got = append(got, db)

		return nil
	})

	if !slices.Equal(got, []*pgfx.DB{dbs["node2"]}) || !errors.Is(err, shard.ErrShardUnavailable) {
		t.Fatalf("calls %v, err %v", got, err)
	}
}

func expectPanic(t *testing.T, run func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("the panic did not propagate")
		}
	}()

	run()
}

func TestWritePanicNeutral(t *testing.T) {
	breaker := pgfx.BreakerConfig{Failures: 1, OpenFor: 50 * time.Millisecond}
	dbs := makeDBs(t, breaker, "a")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:a", "a:0-3"))
	halfOpen(t, dbs["a"], breaker.OpenFor)

	expectPanic(t, func() {
		_, _ = router.Write(context.Background(), 1, func(context.Context, *pgfx.DB) error { panic("boom") })
	})

	if !isProbeAvailable(t, dbs["a"]) {
		t.Fatal("a panic kept the probe")
	}
}

func TestReadSkipsOpenMaster(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "m", "r")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m|r", "a:0-3"))
	openBreaker(t, dbs["m"])

	value, err := router.Read(context.Background(), 1, func(_ context.Context, db *pgfx.DB) (string, error) {
		return db.Name(), nil
	})
	if err != nil || value != "r" {
		t.Fatalf("value %q, err %v", value, err)
	}
}

func TestReadAllOpen(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "m", "r")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m|r", "a:0-3"))
	openBreaker(t, dbs["m"])
	openBreaker(t, dbs["r"])

	calls := 0

	_, err := router.Read(context.Background(), 1, func(context.Context, *pgfx.DB) (int, error) {
		calls++

		return 0, nil
	})
	if calls != 0 || !errors.Is(err, shard.ErrShardUnavailable) || !errors.Is(err, pgfx.ErrBreakerOpen) {
		t.Fatalf("calls %d, err %v", calls, err)
	}
}

func TestReadKeepsUnusedProbe(t *testing.T) {
	breaker := pgfx.BreakerConfig{Failures: 1, OpenFor: 50 * time.Millisecond}
	dbs := makeDBs(t, breaker, "m", "r")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m|r", "a:0-3"))
	openBreaker(t, dbs["m"])
	openBreaker(t, dbs["r"])
	time.Sleep(breaker.OpenFor + 10*time.Millisecond)

	var got []*pgfx.DB

	if _, err := router.Read(context.Background(), 1, func(_ context.Context, db *pgfx.DB) (int, error) {
		got = append(got, db)

		return 1, nil
	}); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got, []*pgfx.DB{dbs["m"]}) {
		t.Fatalf("calls %v", got)
	}

	if !isProbeAvailable(t, dbs["r"]) {
		t.Fatal("the replica's probe was spent although it was not read")
	}
}

func TestReadShard(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "m", "r")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m|r", "a:0-3"))

	shards := router.Shards()
	if len(shards) != 1 || shards[0].Master != dbs["m"] || !slices.Equal(shards[0].Replicas, []*pgfx.DB{dbs["r"]}) {
		t.Fatalf("shards %+v", shards)
	}

	value, err := shards[0].Read(context.Background(), func(_ context.Context, db *pgfx.DB) (string, error) {
		return db.Name(), nil
	})
	if err != nil || value != "m" {
		t.Fatalf("value %q, err %v", value, err)
	}
}

func TestReadPanicNeutral(t *testing.T) {
	breaker := pgfx.BreakerConfig{Failures: 1, OpenFor: 50 * time.Millisecond}
	dbs := makeDBs(t, breaker, "m")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m", "a:0-3"))
	halfOpen(t, dbs["m"], breaker.OpenFor)

	expectPanic(t, func() {
		_, _ = router.Read(context.Background(), 1, func(context.Context, *pgfx.DB) (int, error) { panic("boom") })
	})

	if !isProbeAvailable(t, dbs["m"]) {
		t.Fatal("a panic in Read kept the probe")
	}
}

func TestReadShardPanicNeutral(t *testing.T) {
	breaker := pgfx.BreakerConfig{Failures: 1, OpenFor: 50 * time.Millisecond}
	dbs := makeDBs(t, breaker, "m")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m", "a:0-3"))
	halfOpen(t, dbs["m"], breaker.OpenFor)

	expectPanic(t, func() {
		_, _ = router.Shards()[0].Read(context.Background(), func(context.Context, *pgfx.DB) (int, error) { panic("boom") })
	})

	if !isProbeAvailable(t, dbs["m"]) {
		t.Fatal("a panic in Shard.Read kept the probe")
	}
}

func TestReadUsesRouteSnapshot(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, updateNodes, "b:0-3"))

	paused, release := make(chan struct{}), make(chan struct{})

	var releaseOnce sync.Once

	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	restore := shard.SetAfterRoute(func() {
		close(paused)
		<-release
	})

	var (
		got  string
		done = make(chan struct{})
	)

	t.Cleanup(func() {
		unblock()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Read did not return")
		}

		restore()
	})

	go func() {
		defer close(done)

		got, _ = router.Read(context.Background(), 1, func(_ context.Context, db *pgfx.DB) (string, error) {
			return db.Name(), nil
		})
	}()

	<-paused

	for _, layout := range []*shard.Layout{
		makeLayout(t, 2, 4, updateNodes, "b>a:0-3"),
		makeLayout(t, 3, 4, updateNodes, "a:0-3"),
	} {
		if err := router.Update(layout); err != nil {
			t.Fatal(err)
		}
	}

	unblock()
	<-done

	if got != "b" {
		t.Fatalf("read %q, want the snapshot's source b", got)
	}
}

func TestEachLimit(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b", "c", "d")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:a;b:b;c:c;d:d", "a:0;b:1;c:2;d:3"))

	var running, peak atomic.Int32

	err := router.Each(context.Background(), 2, func(context.Context, shard.Shard) error {
		now := running.Add(1)
		defer running.Add(-1)

		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}

		time.Sleep(20 * time.Millisecond)

		return nil
	})
	if err != nil || peak.Load() > 2 {
		t.Fatalf("peak %d, err %v", peak.Load(), err)
	}
}

func TestEach(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b", "c")
	router := makeRouter(t, dbs, makeLayout(t, 1, 3, "a:a;b:b;c:c", "a:0;b:1;c:2"))
	openBreaker(t, dbs["c"])

	var (
		mu     sync.Mutex
		called []string
	)

	err := router.Each(context.Background(), 0, func(_ context.Context, s shard.Shard) error {
		mu.Lock()
		called = append(called, s.Name)
		mu.Unlock()

		if s.Name == "b" {
			return errApp
		}

		return nil
	})

	slices.Sort(called)

	if !slices.Equal(called, []string{"a", "b", "c"}) || !errors.Is(err, errApp) || !strings.Contains(err.Error(), "shard b") {
		t.Fatalf("called %v, err %v", called, err)
	}
}

func TestEachReadShardMasterDown(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "m", "r")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:m|r", "a:0-3"))
	openBreaker(t, dbs["m"])

	var got string

	err := router.Each(context.Background(), 1, func(ctx context.Context, s shard.Shard) error {
		var err error

		got, err = s.Read(ctx, func(_ context.Context, db *pgfx.DB) (string, error) { return db.Name(), nil })

		return err
	})
	if err != nil || got != "r" {
		t.Fatalf("got %q, err %v", got, err)
	}

	if isProbeAvailable(t, dbs["m"]) {
		t.Fatal("the replica's answer closed the master breaker")
	}
}

type item struct {
	key int64
	tag string
}

func itemKey(it item) int64 { return it.key }

func TestPartitionMigrating(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	router := makeRouter(t, dbs, makeLayout(t, 4, 4, updateNodes, "a:0-1;a>b:2-3"))

	items := []item{{key: 0}, {key: 1}, {key: 2}, {key: 3}}

	partition, err := router.Partition(items, itemKey)
	if err != nil {
		t.Fatal(err)
	}

	if partition.Version != 4 || len(partition.Groups) != 2 {
		t.Fatalf("partition %+v", partition)
	}

	a, b := partition.Groups[0], partition.Groups[1]

	if a.Target.Shard != "a" || !slices.Equal(a.Indexes, []int{0, 1, 2, 3}) || !slices.Equal(a.Items, items) {
		t.Fatalf("group a %+v", a)
	}

	if b.Target.Shard != "b" || b.Target.DB != dbs["b"] || !slices.Equal(b.Indexes, []int{2, 3}) {
		t.Fatalf("group b %+v", b)
	}
}

func TestPartitionSingleSnapshot(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, updateNodes, "a:0-3"))

	called, release := make(chan struct{}), make(chan struct{})

	var once sync.Once

	key := func(it item) int64 {
		once.Do(func() {
			close(called)
			<-release
		})

		return it.key
	}

	type result struct {
		partition shard.Partition[item]
		err       error
	}

	done := make(chan result, 1)

	go func() {
		partition, err := router.Partition([]item{{key: 0}, {key: 3}}, key)
		done <- result{partition: partition, err: err}
	}()

	<-called

	if err := router.Update(makeLayout(t, 2, 4, updateNodes, "a>b:0-3")); err != nil {
		t.Fatal(err)
	}

	close(release)

	got := <-done
	if got.err != nil || got.partition.Version != 1 || len(got.partition.Groups) != 1 || got.partition.Groups[0].Target.Shard != "a" {
		t.Fatalf("partition %+v, err %v", got.partition, got.err)
	}
}

func TestPartitionOutOfRange(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a")
	router, err := shard.MakeRouter(
		shard.MakeBuckets(4, func(k int64) int { return int(k) }),
		makeLayout(t, 1, 4, "a:a", "a:0-3"),
		values(dbs),
	)
	if err != nil {
		t.Fatal(err)
	}

	partition, err := router.Partition([]item{{key: 1}, {key: 9}}, itemKey)
	if !errors.Is(err, shard.ErrLayout) || partition.Groups != nil || partition.Version != 0 {
		t.Fatalf("partition %+v, err %v", partition, err)
	}
}

func TestPartitionOrder(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a", "b")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, updateNodes, "a:0-1;b:2-3"))

	partition, err := router.Partition([]item{{key: 3, tag: "x"}, {key: 0, tag: "y"}, {key: 2, tag: "z"}}, itemKey)
	if err != nil {
		t.Fatal(err)
	}

	if partition.Groups[0].Target.Shard != "a" || partition.Groups[1].Target.Shard != "b" {
		t.Fatalf("groups not sorted: %+v", partition.Groups)
	}

	if tags := []string{partition.Groups[1].Items[0].tag, partition.Groups[1].Items[1].tag}; !slices.Equal(tags, []string{"x", "z"}) {
		t.Fatalf("items out of input order: %v", tags)
	}
}

func TestPartitionEmpty(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a")
	router := makeRouter(t, dbs, makeLayout(t, 5, 4, "a:a", "a:0-3"))

	for _, items := range [][]item{nil, {}} {
		partition, err := router.Partition(items, itemKey)
		if err != nil || partition.Version != 5 || len(partition.Groups) != 0 {
			t.Fatalf("partition %+v, err %v", partition, err)
		}
	}
}

func TestPartitionNilKey(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:a", "a:0-3"))

	for _, items := range [][]item{nil, {{key: 1}}} {
		if _, err := router.Partition(items, nil); err == nil {
			t.Fatal("a nil key was accepted")
		}
	}
}

func TestPartitionKeepsDuplicates(t *testing.T) {
	dbs := makeDBs(t, defaultBreaker, "a")
	router := makeRouter(t, dbs, makeLayout(t, 1, 4, "a:a", "a:0-3"))

	partition, err := router.Partition([]item{{key: 1, tag: "v1"}, {key: 1, tag: "v2"}}, itemKey)
	if err != nil {
		t.Fatal(err)
	}

	group := partition.Groups[0]
	if len(group.Items) != 2 || !slices.Equal(group.Indexes, []int{0, 1}) {
		t.Fatalf("group %+v", group)
	}
}
