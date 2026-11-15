//go:build integration

package pgfx

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

func makePoolHooksTestDB(t *testing.T, mode string, opts ...Option) *DB {
	t.Helper()
	host := os.Getenv("PGFX_TEST_HOST")
	if len(host) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	cfg := makeIntegrationConfig(host, time.Second)
	cfg.Pool.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if mode == "standalone" {
		db, err := MakeDB(ctx, cfg, opts...)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(db.Close)

		return db
	}

	var connection *Connection
	supply := fx.Supply(cfg)
	module := MakeModule(opts...)
	consumer := fx.Populate(&connection)
	if mode == "named" {
		supply = fx.Supply(fx.Annotate(cfg, fx.ResultTags(`name:"replica"`)))
		module = MakeModuleFor("replica", opts...)
		consumer = fx.Invoke(fx.Annotate(
			func(c *Connection) { connection = c },
			fx.ParamTags(`name:"replica"`),
		))
	}

	app := fx.New(
		fx.NopLogger,
		supply,
		module,
		consumer,
	)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()

		if err := app.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	})
	db, err := connection.DB()
	if err != nil {
		t.Fatal(err)
	}

	return db
}

func TestPoolHooksLifecycle(t *testing.T) {
	for _, mode := range []string{"standalone", "default", "named"} {
		t.Run(mode, func(t *testing.T) {
			var isDiscard atomic.Bool
			var pingPID atomic.Uint32
			closed := make(chan uint32, 4) // At most two connections are created in this test.
			db := makePoolHooksTestDB(
				t,
				mode,
				WithPrepareConn(func(ctx context.Context, conn *pgx.Conn) (bool, error) {
					_, err := conn.Exec(ctx, "SELECT set_config('application_name', 'prepared', false)")

					return true, err
				}),
				WithAfterRelease(func(*pgx.Conn) bool { return !isDiscard.Load() }),
				WithBeforeClose(func(conn *pgx.Conn) { closed <- conn.PgConn().PID() }),
				WithShouldPing(func(_ context.Context, params pgxpool.ShouldPingParams) bool {
					pingPID.Store(params.Conn.PgConn().PID())

					return true
				}),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			first, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Release()

			pid := first.Conn().PgConn().PID()
			if pingPID.Load() != pid {
				t.Fatal("ShouldPing did not receive the acquired connection")
			}

			var name string

			if err := first.QueryRow(ctx, "SHOW application_name").Scan(&name); err != nil || name != "prepared" {
				t.Fatalf("PrepareConn session state: %q, %v", name, err)
			}

			first.Release()
			second, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Release()

			if second.Conn().PgConn().PID() != pid {
				t.Fatal("AfterRelease(true) did not retain connection")
			}

			isDiscard.Store(true)
			second.Release()

			select {
			case got := <-closed:
				if got != pid {
					t.Fatalf("BeforeClose PID=%d, want %d", got, pid)
				}
			case <-ctx.Done():
				t.Fatal("AfterRelease(false) did not close connection")
			}

			isDiscard.Store(false)
			third, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer third.Release()

			if third.Conn().PgConn().PID() == pid {
				t.Fatal("discarded connection reused")
			}

			third.Release()
		})
	}
}

func TestPoolPrepareConnOutcomes(t *testing.T) {
	for _, item := range []struct {
		name    string
		isKeep  bool
		isError bool
	}{
		{name: "keep", isKeep: true},
		{name: "retry"},
		{name: "keep_error", isKeep: true, isError: true},
		{name: "discard_error", isError: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			var isArmed atomic.Bool
			var rejectedPID atomic.Uint32
			want := errors.New("prepare rejected")
			db := makePoolHooksTestDB(t, "default", WithPrepareConn(func(_ context.Context, conn *pgx.Conn) (bool, error) {
				if !isArmed.CompareAndSwap(true, false) {
					return true, nil
				}

				rejectedPID.Store(conn.PgConn().PID())
				if item.isError {
					return item.isKeep, want
				}

				return item.isKeep, nil
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			isArmed.Store(true)
			conn, err := db.Acquire(ctx)
			if item.isError {
				if !errors.Is(err, want) {
					if conn != nil {
						conn.Release()
					}

					t.Fatalf("Acquire error: %v", err)
				}

				conn, err = db.Acquire(ctx)
			}

			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()

			if rejectedPID.Load() == 0 {
				t.Fatal("PrepareConn did not run")
			}

			isSame := conn.Conn().PgConn().PID() == rejectedPID.Load()
			if isSame != item.isKeep {
				t.Fatalf("connection retained=%v, want %v", isSame, item.isKeep)
			}
		})
	}
}

func TestPoolShouldPingDecision(t *testing.T) {
	for _, isPing := range []bool{false, true} {
		name := "skip"
		if isPing {
			name = "ping"
		}

		t.Run(name, func(t *testing.T) {
			db := makePoolHooksTestDB(t, "standalone", WithShouldPing(func(context.Context, pgxpool.ShouldPingParams) bool {
				return isPing
			}))
			admin := makePoolHooksTestDB(t, "standalone")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}

			pid := conn.Conn().PgConn().PID()
			conn.Release()
			var isTerminated bool

			if err := admin.QueryRow(ctx, "SELECT pg_terminate_backend($1, 5000)", pid).Scan(&isTerminated); err != nil || !isTerminated {
				t.Fatalf("terminate idle test connection: %v, %v", isTerminated, err)
			}

			conn, err = db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()

			isReplaced := conn.Conn().PgConn().PID() != pid
			if isReplaced != isPing {
				t.Fatalf("terminated connection replaced=%v, want %v", isReplaced, isPing)
			}
		})
	}
}

func TestPoolPrepareConnCancellation(t *testing.T) {
	db := makePoolHooksTestDB(t, "standalone", WithPrepareConn(func(ctx context.Context, _ *pgx.Conn) (bool, error) {
		<-ctx.Done()

		return false, ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn, err := db.Acquire(ctx)
	if conn != nil {
		conn.Release()
		t.Fatal("connection returned after cancellation")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire: %v", err)
	}
}
