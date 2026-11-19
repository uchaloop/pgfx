//go:build integration

package pgfx

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func makeBreakerDB(t *testing.T, cfg Config) *DB {
	t.Helper()

	db, err := MakeDB(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	return db
}

func isBreakerOpen(breaker *Breaker) bool {
	_, attempt, err := breaker.Attempt(context.Background())
	if err != nil {
		return errors.Is(err, ErrBreakerOpen)
	}

	attempt.Release()

	return false
}

func TestBreakerRepeatedStatementTimeoutsIntegration(t *testing.T) {
	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Timeouts.Statement = 50 * time.Millisecond
	cfg.Breaker = BreakerConfig{Failures: 3, OpenFor: time.Minute}
	db := makeBreakerDB(t, cfg)

	for i := range 3 {
		if isBreakerOpen(db.Breaker()) {
			t.Fatalf("opened after %d timeouts", i)
		}

		_, err := db.Exec(t.Context(), "SELECT pg_sleep(1)")
		if !isStatementTimeout(err) {
			t.Fatalf("query %d: %v", i, err)
		}
	}

	if !isBreakerOpen(db.Breaker()) {
		t.Fatal("three statement timeouts with successful acquires did not open the breaker")
	}
}

func isStatementTimeout(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}

func TestAttemptTransactionCountsOnce(t *testing.T) {
	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Breaker = BreakerConfig{Failures: 2, OpenFor: time.Minute}
	db := makeBreakerDB(t, cfg)

	runTimedOutTransaction := func() {
		ctx, attempt, err := db.Breaker().Attempt(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		err = db.Transaction(ctx, pgx.TxOptions{}, func(tx *Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = 50"); err != nil {
				return err
			}

			_, err := tx.Exec(ctx, "SELECT pg_sleep(1)")

			return err
		})

		if !isStatementTimeout(err) {
			t.Fatalf("transaction: %v", err)
		}

		attempt.Done(err)
	}

	runTimedOutTransaction()

	if isBreakerOpen(db.Breaker()) {
		t.Fatal("one failed transaction counted more than once")
	}

	runTimedOutTransaction()

	if !isBreakerOpen(db.Breaker()) {
		t.Fatal("two failed transactions did not open the breaker")
	}
}

func TestBreakerNetworkTimeoutIntegration(t *testing.T) {
	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Breaker = BreakerConfig{Failures: 1, OpenFor: time.Minute}
	db := makeBreakerDB(t, cfg)

	conn, err := db.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := conn.Conn().PgConn().Conn().SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}

	_, err = conn.Exec(t.Context(), "SELECT 1")

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = conn.Conn().Close(closeCtx)
	conn.Release()

	if !pgconn.Timeout(err) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a network timeout, got %v", err)
	}

	if !isBreakerOpen(db.Breaker()) {
		t.Fatal("a network timeout did not open the breaker")
	}
}

func TestBreakerCancelIntegration(t *testing.T) {
	cfg := makeIntegrationConfig(t, time.Second)
	cfg.Breaker = BreakerConfig{Failures: 1, OpenFor: time.Minute}
	db := makeBreakerDB(t, cfg)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := db.Exec(ctx, "SELECT 1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}

	if isBreakerOpen(db.Breaker()) {
		t.Fatal("a cancelled query opened the breaker")
	}
}

// probeUntilClosed runs probe operations until one succeeds, then checks that
// the breaker is closed: two operations in a row are granted.
func probeUntilClosed(t *testing.T, db *DB, deadline time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	for {
		attemptCtx, attempt, err := db.Breaker().Attempt(ctx)
		if err == nil {
			queryCtx, queryCancel := context.WithTimeout(attemptCtx, time.Second)
			_, queryErr := db.Exec(queryCtx, "SELECT 1")
			queryCancel()
			attempt.Done(queryErr)

			if queryErr == nil {
				break
			}
		}

		select {
		case <-ctx.Done():
			t.Fatal("no successful probe before the deadline")
		case <-time.After(50 * time.Millisecond):
		}
	}

	for range 2 {
		if isBreakerOpen(db.Breaker()) {
			t.Fatal("a successful probe did not close the breaker")
		}
	}
}

// failUntilOpen runs failing operations until the breaker refuses them.
func failUntilOpen(t *testing.T, db *DB, deadline time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	for !isBreakerOpen(db.Breaker()) {
		attemptCtx, attempt, err := db.Breaker().Attempt(ctx)
		if err != nil {
			continue
		}

		queryCtx, queryCancel := context.WithTimeout(attemptCtx, time.Second)
		_, queryErr := db.Exec(queryCtx, "SELECT 1")
		queryCancel()
		attempt.Done(queryErr)

		if queryErr == nil {
			t.Fatal("a query succeeded while the server was unreachable")
		}

		if ctx.Err() != nil {
			t.Fatal("the breaker did not open before the deadline")
		}
	}
}

func TestBreakerProxyRecoveryIntegration(t *testing.T) {
	cfg := makeIntegrationConfig(t, 500*time.Millisecond)
	proxy := startTCPProxy(t, cfg.Host)
	cfg.Host = proxy.Addr()
	cfg.Breaker = BreakerConfig{Failures: 2, OpenFor: 200 * time.Millisecond}
	db := makeBreakerDB(t, cfg)

	probeUntilClosed(t, db, 5*time.Second)

	proxy.Stop(t)
	failUntilOpen(t, db, 10*time.Second)

	proxy.Start(t)
	probeUntilClosed(t, db, 10*time.Second)
}

// tcpProxy forwards TCP connections to a target. Stop closes its listener and
// every established connection, so clients see a real transport failure.
type tcpProxy struct {
	target string
	addr   string

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func startTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	proxy := &tcpProxy{target: target, addr: listener.Addr().String(), conns: make(map[net.Conn]struct{})}
	proxy.serve(listener)
	t.Cleanup(func() { proxy.Stop(t) })

	return proxy
}

func (p *tcpProxy) Addr() string {
	return p.addr
}

// Start listens again on the same address.
func (p *tcpProxy) Start(t *testing.T) {
	t.Helper()

	listener, err := net.Listen("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}

	p.serve(listener)
}

// Stop closes the listener and all forwarded connections and waits, with a
// deadline, for the forwarding goroutines to finish.
func (p *tcpProxy) Stop(t *testing.T) {
	t.Helper()

	p.mu.Lock()
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}

	for conn := range p.conns {
		_ = conn.Close()
	}
	p.mu.Unlock()

	done := make(chan struct{})

	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy goroutines did not stop")
	}
}

func (p *tcpProxy) serve(listener net.Listener) {
	p.mu.Lock()
	p.listener = listener
	p.mu.Unlock()

	p.wg.Go(func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}

			upstream, err := net.DialTimeout("tcp", p.target, time.Second)
			if err != nil {
				_ = client.Close()

				continue
			}

			if !p.track(client, upstream) {
				return
			}

			p.wg.Go(func() { p.pipe(client, upstream) })
			p.wg.Go(func() { p.pipe(upstream, client) })
		}
	})
}

// track registers a forwarded pair; it refuses (and closes) the pair when the
// proxy has stopped meanwhile.
func (p *tcpProxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.listener == nil {
		for _, conn := range conns {
			_ = conn.Close()
		}

		return false
	}

	for _, conn := range conns {
		p.conns[conn] = struct{}{}
	}

	return true
}

func (p *tcpProxy) pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)

	p.mu.Lock()
	delete(p.conns, dst)
	delete(p.conns, src)
	p.mu.Unlock()

	_ = dst.Close()
	_ = src.Close()
}

func TestBreakerPostgresRestart(t *testing.T) {
	ctl, data := os.Getenv("PGFX_HEALTH_PG_CTL"), os.Getenv("PGFX_HEALTH_PG_DATA")
	if len(ctl) == 0 || len(data) == 0 {
		t.Skip("requires disposable PGFX_HEALTH_PG_CTL and PGFX_HEALTH_PG_DATA")
	}

	cfg := makeIntegrationConfig(t, 500*time.Millisecond)
	cfg.Breaker = BreakerConfig{Failures: 2, OpenFor: 500 * time.Millisecond}
	db := makeBreakerDB(t, cfg)

	if _, err := db.Exec(t.Context(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}

	isStopped := true
	controlPostgres(t, ctl, data, "stop")

	t.Cleanup(func() {
		if isStopped {
			controlPostgres(t, ctl, data, "start")
		}
	})

	failUntilOpen(t, db, 10*time.Second)

	controlPostgres(t, ctl, data, "start")
	isStopped = false

	waitForServer(t, cfg, 10*time.Second)
	probeUntilClosed(t, db, 10*time.Second)
}

// waitForServer polls a plain connection, independent of any breaker, until
// the server accepts it or the deadline passes.
func waitForServer(t *testing.T, cfg Config, deadline time.Duration) {
	t.Helper()

	poolCfg, err := cfg.poolConfig(makeOptions())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	for {
		conn, err := pgx.ConnectConfig(ctx, poolCfg.ConnConfig)
		if err == nil {
			_ = conn.Close(ctx)

			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("server not ready: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
