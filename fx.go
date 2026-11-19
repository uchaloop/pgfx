package pgfx

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/utilfx"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

// connectionHealth keeps runtime dependencies out of serializable Config.
type connectionHealth struct {
	shutdowner fx.Shutdowner
	logger     *slog.Logger
}

// MakeModule provides an untagged *DB from an untagged Config.
// Configuration is validated during construction. The pool is created on
// start and closed on stop. Start does not wait for the server and does not
// fail because it is unavailable unless WithStartupPing is set. Consumers only store DB in constructors
// and run queries in their OnStart hooks or later. Config.Health enables background checks
// and shutdown on sustained failure. Tracing, query metrics and *slog.Logger
// are optional dependencies. Enabled health checks require HealthModule at
// the application root.
//
// opts use the same runtime options as MakeDB and apply after injected tracing
// and metrics defaults. Later options override earlier settings where applicable;
// nil options are ignored. The option slice is copied when the module is built.
func MakeModule(opts ...Option) fx.Option {
	return fx.Module("pgfx", makeDBProvider("", opts...))
}

// MakeModuleFor is an Fx module for a named connection - a replica or another shard.
// It consumes a pgfx.Config tagged name:"<name>" and provides a *DB tagged
// the same; a consumer selects it with fx.ParamTags. The application supplies
// the tagged Config explicitly, for example with fx.Supply and fx.ResultTags.
// opts configure only this connection, with the same ordering as MakeModule.
// The DB is named after name (WithName is ignored) and is also provided into
// the value group `group:"pgfx"`, so a consumer can receive every named
// connection as []*pgfx.DB and select them by Name.
func MakeModuleFor(name string, opts ...Option) fx.Option {
	return fx.Module("pgfx-"+name, makeDBProvider(name, opts...))
}

// makeDBProvider builds the annotated constructor for a connection whose
// Config carries the given name (empty for the untagged default).
func makeDBProvider(name string, opts ...Option) fx.Option {
	opts = slices.Clone(opts)

	var tag string

	if len(name) != 0 {
		tag = utilfx.NameTag(name)
		opts = append(opts, WithName(name))
	}

	provider := fx.Provide(
		fx.Annotate(
			func(
				cfg Config,
				lifecycle fx.Lifecycle,
				tracerProvider trace.TracerProvider,
				queryMetrics QueryMetricFunc,
				shutdowner fx.Shutdowner,
				logger *slog.Logger,
			) (*DB, error) {
				if logger == nil {
					logger = slog.Default()
				}

				label := name
				if len(label) == 0 {
					label = "default"
				}

				return makeLifecycleDB(
					cfg,
					lifecycle,
					tracerProvider,
					queryMetrics,
					connectionHealth{shutdowner: shutdowner, logger: logger.With("connection", label)},
					opts...,
				)
			},
			fx.ParamTags(
				tag,
				``,
				`optional:"true"`,
				`optional:"true"`,
				``,
				`optional:"true"`,
			),
			fx.ResultTags(tag),
		),
	)

	if len(tag) == 0 {
		return provider
	}

	// Fx cannot tag one result with both a name and a group, so a second
	// provider adds the same named DB to the pgfx group.
	return fx.Options(
		provider,
		fx.Provide(fx.Annotate(
			func(db *DB) *DB { return db },
			fx.ParamTags(tag),
			fx.ResultTags(`group:"pgfx"`),
		)),
	)
}

// makeLifecycleDB builds a handle from the injected Config and optional runtime
// dependencies, and registers its lifecycle.
func makeLifecycleDB(
	cfg Config,
	lifecycle fx.Lifecycle,
	tracerProvider trace.TracerProvider,
	queryMetrics QueryMetricFunc,
	health connectionHealth,
	explicitOpts ...Option,
) (*DB, error) {
	var opts []Option

	if tracerProvider != nil {
		opts = append(opts, WithTrace(otelpgx.WithTracerProvider(tracerProvider)))
	}

	if queryMetrics != nil {
		opts = append(opts, WithQueryMetrics(queryMetrics))
	}

	opts = append(opts, explicitOpts...)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if cfg.Health.IsEnabled {
		if _, isSafe := health.shutdowner.(*healthShutdowner); !isSafe {
			return nil, ErrHealthModuleRequired
		}
	}

	resolvedOpts := makeOptions(opts...)
	state := &connState{name: resolvedOpts.name, breaker: makeBreaker(cfg.Breaker)}
	resolvedOpts.state = state

	poolCfg, err := cfg.poolConfig(resolvedOpts)
	if err != nil {
		return nil, err
	}

	db := &DB{state: state}
	var mu sync.Mutex
	var isStopped bool
	var cancel context.CancelFunc
	var done chan struct{}

	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}

			pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
			if err != nil {
				return err
			}

			if resolvedOpts.isStartupPing {
				if err := pool.Ping(ctx); err != nil {
					pool.Close()

					return err
				}
			}

			mu.Lock()
			if isStopped {
				mu.Unlock()
				pool.Close()

				return context.Canceled
			}

			db.Pool = pool

			if cfg.Health.IsEnabled {
				// The monitor belongs to this connection, not the startup context.
				var monitorCtx context.Context
				monitorCtx, cancel = context.WithCancel(context.Background())
				done = make(chan struct{})

				go func() {
					defer close(done)

					runHealthMonitor(
						monitorCtx,
						cfg.Health,
						pool.Ping,
						func() error {
							mu.Lock()
							defer mu.Unlock()

							if isStopped {
								return nil
							}

							return health.requestShutdown()
						},
						health.logger,
						resolvedOpts.healthSQLStates...,
					)
				}()
			}

			mu.Unlock()

			return nil
		},
		OnStop: func(context.Context) error {
			mu.Lock()
			isStopped = true
			mu.Unlock()

			if cancel != nil {
				cancel()
				<-done
			}

			if db.Pool != nil {
				db.Close()
			}

			return nil
		},
	})

	return db, nil
}

func (h connectionHealth) requestShutdown() error {
	return h.shutdowner.Shutdown(fx.ExitCode(1))
}
