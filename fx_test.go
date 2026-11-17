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

// TestMultiplePoolsGraphNoConflict verifies that the untagged default pool and
// several tagged pools coexist in one container. fx distinguishes them by tag,
// so there is no duplicate-type conflict; ValidateApp fails if there were one.
func TestMultiplePoolsGraphNoConflict(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(makeValidConfig()),
		fx.Supply(fx.Annotate(makeValidConfig(), fx.ResultTags(`name:"replica"`))),
		fx.Supply(fx.Annotate(makeValidConfig(), fx.ResultTags(`name:"analytics"`))),
		MakeModule(),
		MakeModuleFor("replica"),
		MakeModuleFor("analytics"),
		fx.Invoke(fx.Annotate(
			func(primary, replica, analytics *DB) {},
			fx.ParamTags(``, `name:"replica"`, `name:"analytics"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp with three pools: %v", err)
	}
}

// TestMultiplePoolsAreDistinct checks that named and default connections have
// independent identities before their pools start.
func TestMultiplePoolsAreDistinct(t *testing.T) {
	primaryCfg := Config{Host: "primary:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}
	replicaCfg := Config{Host: "replica:5432", Database: "app", TLS: TLSConfig{Mode: "disable"}}

	var primary, replica *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(primaryCfg),
		fx.Supply(fx.Annotate(replicaCfg, fx.ResultTags(`name:"replica"`))),
		MakeModule(),
		MakeModuleFor("replica"),
		fx.Invoke(fx.Annotate(
			func(p, r *DB) { primary, replica = p, r },
			fx.ParamTags(``, `name:"replica"`),
		)),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build two pools: %v", err)
	}

	if primary == nil || replica == nil {
		t.Fatal("a pool was not provided")
	}

	if primary == replica {
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

func TestDBBeforeStartHasNoPool(t *testing.T) {
	var lifecycle capturedLifecycle
	c, err := makeLifecycleDB(
		makeValidConfig(),
		&lifecycle,
		nil,
		nil,
		connectionHealth{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if c.Pool != nil {
		t.Fatal("pool published before successful start")
	}
}

func TestDBConstructionDoesNotCreatePool(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeValidConfig()),
		MakeModule(),
		fx.Invoke(func(c *DB) error {
			if c.Pool != nil {
				t.Fatal("pool allocated during construction")
			}

			return nil
		}),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMakeModuleCancelledStartDoesNotAllocatePool(t *testing.T) {
	var db *DB
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeValidConfig()),
		MakeModule(),
		fx.Populate(&db),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: %v", err)
	}

	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if db.Pool != nil {
		t.Fatal("pool allocated despite cancelled startup")
	}
}

type capturedLifecycle struct{ hooks []fx.Hook }

func (l *capturedLifecycle) Append(hook fx.Hook) { l.hooks = append(l.hooks, hook) }

func TestMakeModulePingFailureDoesNotPublishPool(t *testing.T) {
	var lifecycle capturedLifecycle
	cfg := makeValidConfig()
	cfg.Health = HealthConfig{
		IsEnabled:    true,
		Interval:     time.Second,
		Timeout:      time.Second,
		FailureAfter: time.Minute,
	}
	db, err := makeLifecycleDB(
		cfg,
		&lifecycle,
		nil,
		nil,
		connectionHealth{shutdowner: makeHealthShutdowner(&failedShutdowner{})},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.hooks[0].OnStart(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("OnStart: %v", err)
	}

	if db.Pool != nil {
		t.Fatal("pool published before successful start")
	}
}

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
