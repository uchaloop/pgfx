package pgfx

import (
	"context"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/utilfx"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

// Module is an Fx module for the default Postgres connection: it consumes an
// untagged pgfx.Config from the container and provides an untagged *pgxpool.Pool,
// so a repository just depends on *pgxpool.Pool - no tags, no wrappers.
//
// The application supplies the Config explicitly - pgfx does not read any config
// source itself - typically with confmaker/confx:
//
//	fx.New(
//		confx.LoadModule("config/local.toml"),
//		confx.ProvideDefault[pgfx.Config]("postgres"),
//		pgfx.Module(),
//	)
//
// The pool is pinged on start (fail-fast on a dead database) and closed on stop,
// and takes an optional trace.TracerProvider and QueryMetricFunc from the
// container.
func Module() fx.Option {
	return fx.Module("pgfx", poolProvider(``))
}

// ModuleFor is an Fx module for a named connection - a replica or another shard.
// It consumes a pgfx.Config tagged name:"<name>" and provides a *pgxpool.Pool
// tagged the same; a consumer selects it with fx.ParamTags. The application
// supplies the tagged Config explicitly (typically confmaker/confx's Provide).
func ModuleFor(name string) fx.Option {
	return fx.Module("pgfx-"+name, poolProvider(utilfx.NameTag(name)))
}

// poolProvider builds the annotated pool constructor for a connection whose
// Config carries the given tag (empty for the untagged default).
func poolProvider(tag string) fx.Option {
	return fx.Provide(
		fx.Annotate(
			makePool,
			fx.ParamTags(tag, ``, `optional:"true"`, `optional:"true"`),
			fx.ResultTags(tag),
		),
	)
}

// makePool builds a pool from the injected Config and optional runtime
// dependencies, and registers its lifecycle.
func makePool(
	cfg Config,
	lifecycle fx.Lifecycle,
	tracerProvider trace.TracerProvider,
	queryMetrics QueryMetricFunc,
) (*pgxpool.Pool, error) {
	var opts []Option
	// Tracing is opt-in: enable otelpgx spans only when a TracerProvider is
	// supplied to the container.
	if tracerProvider != nil {
		opts = append(opts, WithTracing(otelpgx.WithTracerProvider(tracerProvider)))
	}
	if queryMetrics != nil {
		opts = append(opts, WithQueryMetrics(queryMetrics))
	}

	pool, err := Make(context.Background(), cfg, opts...)
	if err != nil {
		return nil, err
	}

	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return pool.Ping(ctx)
		},
		OnStop: func(context.Context) error {
			pool.Close()

			return nil
		},
	})

	return pool, nil
}
