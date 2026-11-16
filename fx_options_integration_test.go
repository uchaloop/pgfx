//go:build integration

package pgfx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"
)

func TestFxAfterConnectTypes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Pool.MaxConns = 2
	admin, err := MakeDB(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	if _, err := admin.Exec(ctx, "CREATE TYPE public.pgfx_fx_status AS ENUM ('new', 'paid')"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()

		if _, err := admin.Exec(cleanupCtx, "DROP TYPE public.pgfx_fx_status"); err != nil {
			t.Error(err)
		}
	}()

	register := func(label string) Option {
		return WithAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			for _, name := range []string{"public.pgfx_fx_status", "public._pgfx_fx_status"} {
				typ, err := conn.LoadType(ctx, name)
				if err != nil {
					return err
				}

				conn.TypeMap().RegisterType(typ)
			}

			_, err := conn.Exec(ctx, "SELECT set_config('application_name', $1, false)", label)

			return err
		})
	}
	var primary, replica *Connection
	opts := []Option{register("primary")}
	primaryModule := MakeModule(opts...)
	opts[0] = WithAfterConnect(func(context.Context, *pgx.Conn) error {
		return errors.New("caller mutated options")
	})
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		fx.Supply(fx.Annotate(cfg, fx.ResultTags(`name:"replica"`))),
		primaryModule,
		MakeModuleFor("replica", register("replica")),
		fx.Invoke(fx.Annotate(
			func(p, r *Connection) { primary, replica = p, r },
			fx.ParamTags(``, `name:"replica"`),
		)),
	)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()

		if err := app.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	}()

	for _, item := range []struct {
		name       string
		connection *Connection
	}{
		{name: "primary", connection: primary},
		{name: "replica", connection: replica},
	} {
		t.Run(item.name, func(t *testing.T) {
			db, err := item.connection.DB()
			if err != nil {
				t.Fatal(err)
			}

			for generation := range 2 {
				t.Run(fmt.Sprintf("generation_%d", generation), func(t *testing.T) {
					for range 2 {
						conn, err := db.Acquire(ctx)
						if err != nil {
							t.Fatal(err)
						}
						defer conn.Release()

						if _, isOK := conn.Conn().TypeMap().TypeForName("public.pgfx_fx_status"); !isOK {
							t.Fatal("enum not registered")
						}

						var got []string
						err = conn.QueryRow(ctx, "SELECT $1::public.pgfx_fx_status[]", []string{"new", "paid"}).Scan(&got)
						if err != nil || !slices.Equal(got, []string{"new", "paid"}) {
							t.Fatalf("enum array: %v, %v", got, err)
						}

						var name string

						if err := conn.QueryRow(ctx, "SHOW application_name").Scan(&name); err != nil {
							t.Fatal(err)
						}

						if name != item.name {
							t.Fatalf("hook leaked across pools: %q", name)
						}
					}
				})
				db.Reset()
			}
		})
	}
}

func TestFxAfterConnectFailure(t *testing.T) {
	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Pool.MaxConns = 1

	for _, name := range []string{"", "replica"} {
		t.Run(name, func(t *testing.T) {
			want := errors.New("type registration failed")
			var connection *Connection
			hook := WithAfterConnect(func(context.Context, *pgx.Conn) error { return want })
			module := MakeModule(hook)
			supply := fx.Supply(cfg)
			invoke := fx.Populate(&connection)
			if len(name) != 0 {
				module = MakeModuleFor(name, hook)
				supply = fx.Supply(fx.Annotate(cfg, fx.ResultTags(`name:"replica"`)))
				invoke = fx.Invoke(fx.Annotate(
					func(c *Connection) { connection = c },
					fx.ParamTags(`name:"replica"`),
				))
			}

			app := fx.New(
				fx.NopLogger,
				supply,
				module,
				invoke,
			)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := app.Start(ctx); !errors.Is(err, want) {
				t.Fatalf("Start: %v, want hook error", err)
			}

			if _, err := connection.DB(); !errors.Is(err, ErrConnectionNotReady) {
				t.Fatalf("published failed pool: %v", err)
			}

			if err := app.Stop(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFxAfterConnectCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	var connection *Connection
	cfg := makeIntegrationConfig(t, 100*time.Millisecond)
	cfg.Pool.MaxConns = 1
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		MakeModule(WithAfterConnect(func(ctx context.Context, _ *pgx.Conn) error {
			entered <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}

			return ctx.Err()
		})),
		fx.Populate(&connection),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := app.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start: %v, want deadline", err)
	}

	select {
	case <-entered:
	default:
		t.Fatal("hook did not run")
	}

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("hook did not stop after startup cancellation")
	}

	if _, err := connection.DB(); !errors.Is(err, ErrConnectionNotReady) {
		t.Fatalf("published failed pool: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()

	if err := app.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestFxOptionsOverrideInjectedDefaults(t *testing.T) {
	var injected, explicit atomic.Int64
	var connection *Connection
	app := fx.New(
		fx.NopLogger,
		fx.Supply(makeIntegrationConfig(t, time.Second)),
		fx.Supply(QueryMetricFunc(func(QueryMetric) { injected.Add(1) })),
		MakeModule(
			WithQueryMetrics(func(QueryMetric) { explicit.Add(1) }),
			WithAfterConnect(func(context.Context, *pgx.Conn) error {
				return errors.New("replaced hook must not run")
			}),
			WithAfterConnect(nil),
			nil,
		),
		fx.Populate(&connection),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()

	db, err := connection.DB()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}

	if injected.Load() != 0 || explicit.Load() == 0 {
		t.Fatalf("metrics precedence: injected=%d explicit=%d", injected.Load(), explicit.Load())
	}
}
