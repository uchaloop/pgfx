package pgfx

import (
	"context"

	"github.com/exaring/otelpgx"
	"github.com/uchaloop/utilfx"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

// Module is an Fx module for the default Postgres connection: it consumes an
// untagged pgfx.Config from the container and provides only an untagged *DB, so
// a repository just depends on *DB - no tags, no wrappers. The pool embedded in
// DB is not provided as a separate Fx value.
//
// The application supplies the Config explicitly - pgfx does not read any config
// source itself - typically with confmaker/confx:
//
//	fx.New(
//		confx.Module(),
//		confx.Provide[pgfx.Config]("postgres"),
//		pgfx.Module,
//	)
//
// The connection is pinged on start (fail-fast on a dead database) and closed on
// stop, and takes an optional trace.TracerProvider and QueryMetricFunc from the
// container.
var Module = fx.Module("pgfx", connectionProvider(``))

// ModuleFor is an Fx module for a named connection - a replica or another shard.
// It consumes a pgfx.Config tagged name:"<name>" and provides only a *DB tagged
// the same; a consumer selects it with fx.ParamTags. The application supplies
// the tagged Config explicitly (typically confmaker/confx's Provide).
func ModuleFor(name string) fx.Option {
	return fx.Module("pgfx-"+name, connectionProvider(utilfx.NameTag(name)))
}

// connectionProvider builds the annotated constructor for a connection whose
// Config carries the given tag (empty for the untagged default).
func connectionProvider(tag string) fx.Option {
	return fx.Provide(
		fx.Annotate(
			makeConnection,
			fx.ParamTags(tag, ``, `optional:"true"`, `optional:"true"`),
			fx.ResultTags(tag),
		),
	)
}

// makeConnection builds a DB from the injected Config and optional runtime
// dependencies, and registers its lifecycle.
func makeConnection(
	cfg Config,
	lifecycle fx.Lifecycle,
	tracerProvider trace.TracerProvider,
	queryMetrics QueryMetricFunc,
) (*DB, error) {
	var opts []Option
	// Tracing is opt-in: enable otelpgx spans only when a TracerProvider is
	// supplied to the container.
	if tracerProvider != nil {
		opts = append(opts, WithTracing(otelpgx.WithTracerProvider(tracerProvider)))
	}
	if queryMetrics != nil {
		opts = append(opts, WithQueryMetrics(queryMetrics))
	}

	db, err := Make(context.Background(), cfg, opts...)
	if err != nil {
		return nil, err
	}

	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return db.Ping(ctx)
		},
		OnStop: func(context.Context) error {
			db.Close()

			return nil
		},
	})

	return db, nil
}
