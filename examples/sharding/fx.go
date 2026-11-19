package main

import (
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
	"go.uber.org/fx"
)

func makeLayoutConfig() (shard.LayoutConfig, error) {
	first, err := shard.ParseBucketSet("0-511")
	if err != nil {
		return shard.LayoutConfig{}, err
	}

	second, err := shard.ParseBucketSet("512-1023")
	if err != nil {
		return shard.LayoutConfig{}, err
	}

	return shard.LayoutConfig{
		Version: 1,
		Buckets: 1024,
		Nodes:   []shard.NodeSpec{{Shard: "a", Master: "a"}, {Shard: "b", Master: "b"}},
		Ranges: []shard.RangeSpec{
			{Shard: "a", Buckets: first},
			{Shard: "b", Buckets: second},
		},
	}, nil
}

func makeApp(cfgA, cfgB pgfx.Config, cfg shard.LayoutConfig, router **shard.Router[int64], opts ...pgfx.Option) *fx.App {
	return fx.New(
		fx.NopLogger,
		fx.Supply(
			fx.Annotate(cfgA, fx.ResultTags(`name:"a"`)),
			fx.Annotate(cfgB, fx.ResultTags(`name:"b"`)),
		),
		// Registration order deliberately differs from layout order. Names, not
		// positions in the unordered Fx value group, select each connection.
		pgfx.MakeModuleFor("b", opts...),
		pgfx.MakeModuleFor("a", opts...),
		fx.Provide(fx.Annotate(
			func(dbs []*pgfx.DB) (*shard.Router[int64], error) {
				layout, err := shard.MakeLayout(cfg)
				if err != nil {
					return nil, err
				}

				return shard.MakeRouter(shard.ModuloBuckets[int64](cfg.Buckets), layout, dbs)
			},
			fx.ParamTags(`group:"pgfx"`),
		)),
		fx.Populate(router),
	)
}
