//go:build integration

package pgfx

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/fx"
)

func TestHealthSQLStateConnectError(t *testing.T) {
	cfg := makeHealthIntegrationConfig(t)
	cfg.Database = "pgfx_missing_health_policy_database"
	poolCfg, err := cfg.poolConfig(makeOptions())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgconn.ConnectConfig(ctx, &poolCfg.ConnConfig.Config)
	if conn != nil {
		defer conn.Close(ctx)
	}
	if _, isOK := errors.AsType[*pgconn.ConnectError](err); !isOK {
		t.Fatalf("expected ConnectError, got %T", err)
	}
	if state := SQLState(err); state != "3D000" {
		t.Fatalf("SQLSTATE = %q", state)
	}
	if category, _ := classifyHealthError(err, nil); len(category) != 0 {
		t.Fatal("ConnectError bypassed SQLSTATE policy")
	}
	if category, code := classifyHealthError(err, DefaultHealthSQLStates()); category != "sqlstate" || code != "3D000" {
		t.Fatalf("classification = %q, %q", category, code)
	}
}

func TestHealthSQLStateNamedPools(t *testing.T) {
	cfg := makeHealthIntegrationConfig(t)
	cfg.Health.Timeout = 100 * time.Millisecond
	cfg.Health.Interval = 100 * time.Millisecond
	cfg.Health.FailureAfter = 300 * time.Millisecond
	var stage atomic.Int32
	var primaryChecks, replicaChecks atomic.Int32
	hook := func(isPrimary bool) Option {
		return WithPrepareConn(func(context.Context, *pgx.Conn) (bool, error) {
			if stage.Load() == 0 {
				return true, nil
			}

			if isPrimary {
				primaryChecks.Add(1)
				if stage.Load() == 2 {
					return true, &pgconn.PgError{Code: "57P03"}
				}

				return true, &pgconn.PgError{Code: "53300"}
			}
			replicaChecks.Add(1)

			return true, &pgconn.PgError{Code: "57P03"}
		})
	}
	app := fx.New(
		fx.NopLogger,
		HealthModule,
		fx.Supply(cfg, fx.Annotate(cfg, fx.ResultTags(`name:"replica"`))),
		MakeModule(WithHealthSQLStates("57P03"), hook(true)),
		MakeModuleFor("replica", WithHealthSQLStates("53300"), hook(false)),
		fx.Invoke(fx.Annotate(func(*Connection, *Connection) {}, fx.ParamTags(``, `name:"replica"`))),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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

	signal := app.Wait()
	stage.Store(1)
	select {
	case <-signal:
		t.Fatal("policy leaked between named connections")
	case <-time.After(time.Second):
	}
	if primaryChecks.Load() < 2 || replicaChecks.Load() < 2 {
		t.Fatal("monitors did not run")
	}

	stage.Store(2)
	select {
	case result := <-signal:
		if result.ExitCode != 1 {
			t.Fatalf("exit code = %d", result.ExitCode)
		}
	case <-ctx.Done():
		t.Fatal("matching SQLSTATE did not trigger shutdown")
	}
}

func TestHealthSQLStateStartupNotFiltered(t *testing.T) {
	cfg := makeHealthIntegrationConfig(t)
	var connection *Connection
	app := fx.New(
		fx.NopLogger,
		HealthModule,
		fx.Supply(cfg),
		MakeModule(WithHealthSQLStates(), WithPrepareConn(func(context.Context, *pgx.Conn) (bool, error) {
			return true, &pgconn.PgError{Code: "53300"}
		})),
		fx.Populate(&connection),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := app.Start(ctx)
	if err == nil {
		_ = app.Stop(ctx)
		t.Fatal("startup ignored SQLSTATE")
	}
	if SQLState(err) != "53300" {
		t.Fatalf("startup error: %v", err)
	}
	if _, err := connection.DB(); !errors.Is(err, ErrConnectionNotReady) {
		t.Fatalf("published DB after failed startup: %v", err)
	}
}

func TestHealthConnectHookErrors(t *testing.T) {
	for _, hook := range []string{"AfterConnect", "ValidateConnect", "AfterNetConnect"} {
		t.Run(hook, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				err      error
				category string
				code     string
			}{
				{name: "unknown", err: errors.New("application hook failed")},
				{
					name:     "selected SQLSTATE",
					err:      &pgconn.PgError{Code: "57P03"},
					category: "sqlstate",
					code:     "57P03",
				},
				{name: "ignored SQLSTATE", err: &pgconn.PgError{Code: "53300"}},
				{name: "transport", err: io.EOF, category: "connection"},
				{name: "network", err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, category: "connection"},
				{name: "TLS", err: tls.RecordHeaderError{}, category: "tls"},
				{name: "timeout", err: context.DeadlineExceeded, category: "timeout"},
				{name: "joined", err: errors.Join(errors.New("application hook failed"), io.EOF), category: "connection"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := makeHealthIntegrationConfig(t)
					db, err := MakeDB(t.Context(), cfg, WithBeforeConnect(func(_ context.Context, cfg *pgx.ConnConfig) error {
						callback := func(context.Context, *pgconn.PgConn) error { return tc.err }

						if hook == "AfterConnect" {
							cfg.Config.AfterConnect = callback
						} else if hook == "ValidateConnect" {
							cfg.ValidateConnect = callback
						} else {
							cfg.AfterNetConnect = func(_ context.Context, _ *pgconn.Config, conn net.Conn) (net.Conn, error) {
								return conn, tc.err
							}
						}

						return nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()

					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					err = db.Ping(ctx)
					if !errors.Is(err, tc.err) {
						t.Fatalf("hook error was not preserved: %v", err)
					}
					if _, isOK := errors.AsType[*pgconn.ConnectError](err); !isOK {
						t.Fatalf("expected ConnectError, got %T", err)
					}

					category, code := classifyHealthError(err, DefaultHealthSQLStates())
					if category != tc.category || code != tc.code {
						t.Fatalf(
							"classification = (%q, %q), want (%q, %q)",
							category,
							code,
							tc.category,
							tc.code,
						)
					}
				})
			}
		})
	}
}

func TestHealthTLSRefusal(t *testing.T) {
	cfg := makeHealthIntegrationConfig(t)
	db, err := MakeDB(
		t.Context(),
		cfg,
		WithTLS(&tls.Config{ServerName: "localhost"}),
		WithBeforeConnect(func(_ context.Context, cfg *pgx.ConnConfig) error {
			cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
				client, server := net.Pipe()

				go func() {
					defer server.Close()

					var request [8]byte

					if _, err := io.ReadFull(server, request[:]); err == nil {
						_, _ = server.Write([]byte{'N'})
					}
				}()

				return client, nil
			}

			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err = db.Ping(ctx)
	if _, isOK := errors.AsType[*pgconn.ConnectError](err); !isOK {
		t.Fatalf("expected ConnectError, got %v", err)
	}
	if category, _ := classifyHealthError(err, nil); len(category) == 0 {
		t.Fatal("TLS refusal was ignored")
	}
}
