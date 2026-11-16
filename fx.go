package pgfx

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/utilfx"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

// ErrConnectionNotReady reports access before successful lifecycle startup.
var ErrConnectionNotReady = errors.New("connection is not ready")

// ErrConnectionClosed reports access after lifecycle shutdown.
var ErrConnectionClosed = errors.New("connection is closed")

// Connection is an Fx-owned pool handle. Its zero value is not ready.
// A Connection must not be copied after first use.
type Connection struct {
	mu           sync.RWMutex
	db           *DB
	isClosed     bool
	healthCancel context.CancelFunc
	healthDone   <-chan struct{}
}

// connectionHealth keeps runtime dependencies out of serializable Config.
type connectionHealth struct {
	shutdowner fx.Shutdowner
	logger     *slog.Logger
}

// MakeModule provides an untagged *Connection from an untagged Config.
// Configuration is validated during construction. The pool is created and
// pinged on start, then closed on stop. Consumers call Connection.DB in their
// OnStart hooks or later. Config.Health optionally enables background checks
// and shutdown on sustained failure. Tracing, query metrics and *slog.Logger
// are optional dependencies. Enabled health checks require HealthModule at
// the application root.
//
// opts use the same runtime options as MakeDB and apply after injected tracing
// and metrics defaults. Later options override earlier settings where applicable;
// nil options are ignored. The option slice is copied when the module is built.
func MakeModule(opts ...Option) fx.Option {
	return fx.Module("pgfx", makeConnectionProvider("", opts...))
}

// MakeModuleFor is an Fx module for a named connection - a replica or another shard.
// It consumes a pgfx.Config tagged name:"<name>" and provides a *Connection tagged
// the same; a consumer selects it with fx.ParamTags. The application supplies
// the tagged Config explicitly, for example with fx.Supply and fx.ResultTags.
// opts configure only this connection, with the same ordering as MakeModule.
func MakeModuleFor(name string, opts ...Option) fx.Option {
	return fx.Module("pgfx-"+name, makeConnectionProvider(name, opts...))
}

// makeConnectionProvider builds the annotated constructor for a connection whose
// Config carries the given name (empty for the untagged default).
func makeConnectionProvider(name string, opts ...Option) fx.Option {
	opts = slices.Clone(opts)

	var tag string

	if len(name) != 0 {
		tag = utilfx.NameTag(name)
	}

	return fx.Provide(
		fx.Annotate(
			func(
				cfg Config,
				lifecycle fx.Lifecycle,
				tracerProvider trace.TracerProvider,
				queryMetrics QueryMetricFunc,
				shutdowner fx.Shutdowner,
				logger *slog.Logger,
			) (*Connection, error) {
				if logger == nil {
					logger = slog.Default()
				}

				label := name
				if len(label) == 0 {
					label = "default"
				}

				return makeConnection(
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
}

// makeConnection builds a handle from the injected Config and optional runtime
// dependencies, and registers its lifecycle.
func makeConnection(
	cfg Config,
	lifecycle fx.Lifecycle,
	tracerProvider trace.TracerProvider,
	queryMetrics QueryMetricFunc,
	health connectionHealth,
	explicitOpts ...Option,
) (*Connection, error) {
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
	poolCfg, err := cfg.poolConfig(resolvedOpts)
	if err != nil {
		return nil, err
	}

	connection := &Connection{}

	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
			if err != nil {
				return err
			}

			if err := pool.Ping(ctx); err != nil {
				pool.Close()

				return err
			}

			connection.mu.Lock()
			if connection.isClosed {
				connection.mu.Unlock()
				pool.Close()

				return ErrConnectionClosed
			}

			connection.db = &DB{Pool: pool}

			if cfg.Health.IsEnabled {
				// The monitor belongs to this connection, not the startup context.
				monitorCtx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				connection.healthCancel = cancel
				connection.healthDone = done

				go func() {
					defer close(done)

					runHealthMonitor(
						monitorCtx,
						cfg.Health,
						pool.Ping,
						func() error {
							connection.mu.Lock()
							defer connection.mu.Unlock()

							if connection.isClosed {
								return nil
							}

							return health.requestShutdown()
						},
						health.logger,
						resolvedOpts.healthSQLStates...,
					)
				}()
			}

			connection.mu.Unlock()

			return nil
		},
		OnStop: func(context.Context) error {
			connection.close()

			return nil
		},
	})

	return connection, nil
}

// DB returns a borrowed, initialized database after successful startup.
// The caller must not close it. Shutdown closes the pool and waits for acquired
// connections to be released; a previously returned DB does not extend its life.
func (c *Connection) DB() (*DB, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.isClosed {
		return nil, ErrConnectionClosed
	}

	if c.db == nil {
		return nil, ErrConnectionNotReady
	}

	return c.db, nil
}

func (c *Connection) close() {
	c.mu.Lock()
	c.isClosed = true
	db := c.db
	c.db = nil
	cancel, done := c.healthCancel, c.healthDone
	c.mu.Unlock()

	if cancel != nil {
		cancel()
		<-done
	}

	if db != nil {
		db.Close()
	}
}

func (h connectionHealth) requestShutdown() error {
	return h.shutdowner.Shutdown(fx.ExitCode(1))
}
