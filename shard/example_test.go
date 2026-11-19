package shard_test

import (
	"context"
	"fmt"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
)

func ExampleMakeRouter() {
	// Lazy pools let this routing-only example run without PostgreSQL.
	cfg := pgfx.Config{
		Host:     "localhost:5432",
		Database: "example",
		TLS:      pgfx.TLSConfig{Mode: "disable"},
		Breaker:  pgfx.BreakerConfig{Failures: 2, OpenFor: time.Second},
	}

	a, err := pgfx.MakeDB(context.Background(), cfg, pgfx.WithName("a"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer a.Close()

	b, err := pgfx.MakeDB(context.Background(), cfg, pgfx.WithName("b"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer b.Close()

	var spec shard.RangeSpec
	if err := spec.UnmarshalText([]byte("a>b:0-1023")); err != nil {
		fmt.Println(err)
		return
	}

	layout, err := shard.MakeLayout(shard.LayoutConfig{
		Version: 2,
		Buckets: 1024,
		Nodes:   []shard.NodeSpec{{Shard: "a", Master: "a"}, {Shard: "b", Master: "b"}},
		Ranges:  []shard.RangeSpec{spec},
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	router, err := shard.MakeRouter(shard.ModuloBuckets[int64](1024), layout, []*pgfx.DB{b, a})
	if err != nil {
		fmt.Println(err)
		return
	}

	route, err := router.Route(42)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(
		route.Version,
		route.Bucket,
		route.Writes[0].Shard,
		route.Writes[1].Shard,
	)
	// Output: 2 42 a b
}
