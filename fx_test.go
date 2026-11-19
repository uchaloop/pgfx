package pgfx

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/fx"
)

func makeValidConfig() Config {
	return Config{Host: "127.0.0.1:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}
}

// TestMultiplePoolsAreDistinct checks that named and default connections have
// independent identities before their pools start.
func TestMultiplePoolsAreDistinct(t *testing.T) {
	primaryCfg := Config{Host: "primary:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}
	replicaCfg := Config{Host: "replica:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}

	var primary, replica, analytics *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(primaryCfg),
		fx.Supply(fx.Annotate(replicaCfg, fx.ResultTags(`name:"replica"`))),
		fx.Supply(fx.Annotate(makeValidConfig(), fx.ResultTags(`name:"analytics"`))),
		MakeModule(),
		MakeModuleFor("replica"),
		MakeModuleFor("analytics"),
		fx.Invoke(fx.Annotate(
			func(p, r, a *DB) { primary, replica, analytics = p, r, a },
			fx.ParamTags(``, `name:"replica"`, `name:"analytics"`),
		)),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build three pools: %v", err)
	}

	if primary == nil || replica == nil || analytics == nil {
		t.Fatal("a pool was not provided")
	}

	if primary == replica || primary == analytics || replica == analytics {
		t.Fatal("the default and named connection resolved to the same pool")
	}
}

// TestMakeModuleBuildsWithoutPassword verifies that optional credentials do not
// prevent constructing the application before startup.
func TestMakeModuleBuildsWithoutPassword(t *testing.T) {
	cfg := Config{Host: "h", Database: "app", TLS: TLSConfig{Mode: "disable"}}

	var pool *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		MakeModule(),
		fx.Invoke(func(p *DB) { pool = p }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("passwordless build: %v", err)
	}

	if pool.Pool != nil {
		t.Fatal("pool published before successful start")
	}
}

// TestMakeModuleRequiresConfig verifies the graph fails when no Config is provided.
func TestMakeModuleRequiresConfig(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		MakeModule(),
		fx.Invoke(func(*DB) {}),
	)
	if err == nil {
		t.Fatal("expected ValidateApp to fail without a Config")
	}
}

func TestMakeModuleCancelledStartDoesNotAllocatePool(t *testing.T) {
	for _, tc := range []struct {
		name   string
		health HealthConfig
	}{
		{name: "health disabled"},
		{name: "health enabled", health: HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      time.Second,
			FailureAfter: time.Minute,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := makeValidConfig()
			cfg.Health = tc.health

			var db *DB

			app := fx.New(
				fx.NopLogger,
				fx.Supply(cfg),
				HealthModule,
				MakeModule(),
				fx.Populate(&db),
			)
			if err := app.Err(); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if tc.health.IsEnabled {
				var lifecycle capturedLifecycle

				direct, err := makeLifecycleDB(
					cfg,
					&lifecycle,
					nil,
					nil,
					connectionHealth{shutdowner: makeHealthShutdowner(&failedShutdowner{})},
				)
				if err != nil {
					t.Fatal(err)
				}

				if err := lifecycle.hooks[0].OnStart(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("OnStart: %v", err)
				}

				if direct.Pool != nil {
					t.Fatal("pool allocated by OnStart despite cancelled startup")
				}
			}

			if err := app.Start(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Start: %v", err)
			}

			if err := app.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}

			if db.Pool != nil {
				t.Fatal("pool allocated despite cancelled startup")
			}
		})
	}
}

type capturedLifecycle struct{ hooks []fx.Hook }

func (l *capturedLifecycle) Append(hook fx.Hook) { l.hooks = append(l.hooks, hook) }

func TestMakeModuleEarlierHookFailureDoesNotAllocatePool(t *testing.T) {
	errExpected := errors.New("earlier hook failed")
	var db *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeValidConfig()),
		MakeModule(),
		fx.Invoke(func(lifecycle fx.Lifecycle) {
			lifecycle.Append(fx.Hook{OnStart: func(context.Context) error { return errExpected }})
		}),
		fx.Populate(&db),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}

	if err := app.Start(context.Background()); !errors.Is(err, errExpected) {
		t.Fatalf("Start: %v", err)
	}

	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if db.Pool != nil {
		t.Fatal("pool allocated after an earlier hook failed")
	}
}

func TestModuleStartsLazily(t *testing.T) {
	unreachable := Config{Host: "127.0.0.1:1", Database: "x", TLS: TLSConfig{Mode: "disable"}}

	for _, tc := range []struct {
		name      string
		opts      []Option
		isFailure bool
	}{
		{name: "lazy"},
		{name: "startup ping", opts: []Option{WithStartupPing()}, isFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lifecycle capturedLifecycle

			db, err := makeLifecycleDB(unreachable, &lifecycle, nil, nil, connectionHealth{}, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err = lifecycle.hooks[0].OnStart(ctx)
			if (err != nil) != tc.isFailure {
				t.Fatalf("OnStart: %v", err)
			}

			if !tc.isFailure && db.Pool == nil {
				t.Fatal("lazy start did not publish the pool")
			}

			if err := lifecycle.hooks[0].OnStop(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
