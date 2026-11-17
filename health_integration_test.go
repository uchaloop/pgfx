//go:build integration

package pgfx

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
)

func TestHealthFxPoolUnavailable(t *testing.T) {
	for _, name := range []string{"", "replica"} {
		t.Run("connection_"+name, func(t *testing.T) {
			cfg := makeHealthIntegrationConfig(t)
			var connection *DB
			var logs bytes.Buffer
			app := makeHealthTestApp(
				cfg,
				name,
				&connection,
				&logs,
			)
			if err := app.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := app.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			}()

			db := connection

			acquired, err := db.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer acquired.Release()

			select {
			case signal := <-app.Wait():
				if signal.ExitCode != 1 {
					t.Fatalf("exit code=%d", signal.ExitCode)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no shutdown for unavailable pool")
			}

			acquired.Release()
		})
	}
}

func TestHealthFxRecoveryAndDisabled(t *testing.T) {
	for _, isEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovery", false: "disabled"}[isEnabled], func(t *testing.T) {
			cfg := makeHealthIntegrationConfig(t)
			cfg.Health.IsEnabled = isEnabled
			var connection *DB
			var logs bytes.Buffer
			app := makeHealthTestApp(
				cfg,
				"",
				&connection,
				&logs,
			)
			if err := app.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := app.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			}()

			db := connection

			acquired, err := db.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer acquired.Release()

			time.Sleep(80 * time.Millisecond)
			if isEnabled {
				acquired.Release()
			}

			time.Sleep(400 * time.Millisecond)

			select {
			case signal := <-app.Wait():
				t.Fatalf("unexpected shutdown: %v", signal)
			default:
			}

			acquired.Release()
			if err := app.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}

			if isEnabled && !strings.Contains(logs.String(), "database health restored") {
				t.Fatalf("missing recovery: %s", &logs)
			}

			if !isEnabled && logs.Len() != 0 {
				t.Fatalf("disabled monitor logged: %s", &logs)
			}
		})
	}
}

// TestHealthPostgresRestart requires an explicitly supplied disposable cluster.
// It stops and restarts that server; never point it at a shared database.
func TestHealthPostgresRestart(t *testing.T) {
	ctl, data := os.Getenv("PGFX_HEALTH_PG_CTL"), os.Getenv("PGFX_HEALTH_PG_DATA")
	if len(ctl) == 0 || len(data) == 0 {
		t.Skip("requires disposable PGFX_HEALTH_PG_CTL and PGFX_HEALTH_PG_DATA")
	}

	cfg := makeHealthIntegrationConfig(t)
	cfg.Health.FailureAfter = 3 * time.Second
	var connection *DB
	var logs bytes.Buffer
	app := makeHealthTestApp(
		cfg,
		"",
		&connection,
		&logs,
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	var isStopped bool
	control := func(action string) {
		t.Helper()
		args := []string{
			"-D",
			data,
			"-w",
			action,
		}
		if action == "stop" {
			args = append(args, "-m", "fast")
		}

		if action == "start" {
			args = append(args, "-l", data+"/health-test.log")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if output, err := exec.CommandContext(ctx, ctl, args...).CombinedOutput(); err != nil {
			t.Fatalf(
				"pg_ctl %s: %v\n%s",
				action,
				err,
				output,
			)
		}

		isStopped = action == "stop"
	}
	control("stop")
	defer func() {
		if isStopped {
			control("start")
		}
	}()

	time.Sleep(100 * time.Millisecond)
	control("start")
	db := connection

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for {
		if err := db.Ping(ctx); err == nil {
			break
		}

		if ctx.Err() != nil {
			t.Fatal("pool did not recover")
		}

		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(3200 * time.Millisecond)

	select {
	case signal := <-app.Wait():
		t.Fatalf("shutdown after recovery: %v", signal)
	default:
	}

	control("stop")

	select {
	case signal := <-app.Wait():
		if signal.ExitCode != 1 {
			t.Fatalf("exit code=%d", signal.ExitCode)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("no shutdown during prolonged outage")
	}

	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := connection.Ping(context.Background()); err == nil {
		t.Fatal("pool remained open")
	}

	if !strings.Contains(logs.String(), "database health restored") {
		t.Fatal("recovery not observed")
	}
}

func makeHealthIntegrationConfig(t *testing.T) Config {
	t.Helper()
	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Pool.MaxConns = 1
	cfg.Health = HealthConfig{
		IsEnabled:    true,
		Interval:     20 * time.Millisecond,
		Timeout:      10 * time.Millisecond,
		FailureAfter: 200 * time.Millisecond,
	}

	return cfg
}

func makeHealthTestApp(cfg Config, name string, connection **DB, logs *bytes.Buffer) *fx.App {
	logger := slog.New(slog.NewTextHandler(logs, nil))
	if len(name) == 0 {
		return fx.New(
			fx.NopLogger,
			HealthModule,
			fx.Supply(cfg, logger),
			MakeModule(WithHealthSQLStates(DefaultHealthSQLStates()...)),
			fx.Populate(connection),
		)
	}

	return fx.New(
		fx.NopLogger,
		HealthModule,
		fx.Supply(fx.Annotate(cfg, fx.ResultTags(`name:"`+name+`"`)), logger),
		MakeModuleFor(name, WithHealthSQLStates(DefaultHealthSQLStates()...)),
		fx.Invoke(fx.Annotate(func(c *DB) { *connection = c }, fx.ParamTags(`name:"`+name+`"`))),
	)
}

func TestHealthFxMultiplePools(t *testing.T) {
	cfg := makeHealthIntegrationConfig(t)
	var primary, replica *DB
	app := fx.New(
		fx.NopLogger,
		HealthModule,
		fx.Supply(cfg, fx.Annotate(cfg, fx.ResultTags(`name:"replica"`))),
		MakeModule(WithHealthSQLStates(DefaultHealthSQLStates()...)),
		MakeModuleFor("replica"),
		fx.Invoke(fx.Annotate(func(p, r *DB) { primary, replica = p, r }, fx.ParamTags(``, `name:"replica"`))),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	for _, c := range []*DB{primary, replica} {
		db := c

		acquired, err := db.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer acquired.Release()
	}

	select {
	case signal := <-app.Wait():
		if signal.ExitCode != 1 {
			t.Fatalf("exit code=%d", signal.ExitCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no shutdown")
	}
}

func TestHealthFxStopCancelsMonitorBeforePoolClose(t *testing.T) {
	cfg := makeHealthIntegrationConfig(t)
	cfg.Health.Interval = time.Second
	cfg.Health.Timeout = time.Second
	cfg.Health.FailureAfter = time.Minute
	var connection *DB
	var logs bytes.Buffer
	app := makeHealthTestApp(
		cfg,
		"",
		&connection,
		&logs,
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	db := connection

	acquired, err := db.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Release()

	time.Sleep(1100 * time.Millisecond)
	stopped := make(chan error, 1)
	go func() { stopped <- app.Stop(context.Background()) }()

	select {
	case <-app.Wait():
		t.Fatal("normal stop requested shutdown")
	case <-time.After(2 * time.Second):
	}

	acquired.Release()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}

	if logs.Len() != 0 {
		t.Fatalf("normal stop reported failure: %s", &logs)
	}

	select {
	case signal := <-app.Wait():
		t.Fatalf("normal stop requested shutdown: %v", signal)
	default:
	}
}
