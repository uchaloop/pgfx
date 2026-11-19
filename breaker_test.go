package pgfx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// testClock is a manual clock for breaker tests.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func makeTestBreaker(t *testing.T, cfg BreakerConfig) (*Breaker, *testClock) {
	t.Helper()

	clock := &testClock{now: time.Unix(1_700_000_000, 0)}

	breaker := makeBreaker(cfg)
	breaker.now = clock.Now

	return breaker, clock
}

// finish runs one full attempt that ends with err.
func finish(t *testing.T, breaker *Breaker, err error) {
	t.Helper()

	_, attempt, attemptErr := breaker.Attempt(context.Background())
	if attemptErr != nil {
		t.Fatalf("Attempt: %v", attemptErr)
	}

	attempt.Done(err)
}

func isRefused(breaker *Breaker) bool {
	_, attempt, err := breaker.Attempt(context.Background())
	if err == nil {
		attempt.Release()

		return false
	}

	return errors.Is(err, ErrBreakerOpen)
}

func TestBreakerConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		breaker   BreakerConfig
		isInvalid bool
	}{
		{name: "disabled", breaker: BreakerConfig{}},
		{name: "enabled", breaker: BreakerConfig{Failures: 3, OpenFor: time.Second}},
		{name: "negative failures", breaker: BreakerConfig{Failures: -1}, isInvalid: true},
		{name: "missing open time", breaker: BreakerConfig{Failures: 3}, isInvalid: true},
		{name: "negative open time", breaker: BreakerConfig{OpenFor: -1}, isInvalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Host: "127.0.0.1:1", Database: "x", TLS: TLSConfig{Mode: "disable"}, Breaker: tc.breaker}

			err := cfg.Validate()
			if (err != nil) != tc.isInvalid || (err != nil && !strings.Contains(err.Error(), "breaker")) {
				t.Fatalf("Validate = %v", err)
			}

			if tc.isInvalid {
				return
			}

			db, err := MakeDB(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			if (db.Breaker() != nil) != (tc.breaker.Failures > 0) {
				t.Fatalf("Breaker() = %v", db.Breaker())
			}
		})
	}

	if (&DB{}).Breaker() != nil {
		t.Fatal("a DB without state has a breaker")
	}
}

func TestClassifyPriority(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want outcome
	}{
		{name: "harmless server error and lost connection", err: errors.Join(&pgconn.PgError{Code: "42P01"}, &pgconn.ConnectError{}), want: outcomeFailure},
		{name: "canceled wins", err: errors.Join(context.Canceled, &pgconn.ConnectError{}), want: outcomeNeutral},
		{name: "server answered", err: &pgconn.PgError{Code: "23505"}, want: outcomeSuccess},
		{name: "application error", err: errors.New("decode"), want: outcomeNeutral},
		{name: "nil", want: outcomeSuccess},
		{name: "statement timeout", err: &pgconn.PgError{Code: "57014"}, want: outcomeFailure},
		{name: "deadline", err: context.DeadlineExceeded, want: outcomeFailure},
		{name: "unavailable server", err: &pgconn.PgError{Code: "57P01"}, want: outcomeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Fatalf("classify(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyScanArgBoundary(t *testing.T) {
	for _, err := range []error{
		pgx.ScanArgError{Err: io.EOF},
		pgx.ScanArgError{Err: &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}},
		pgx.ScanArgError{Err: &pgconn.PgError{Code: "42P01"}},
	} {
		if got := classify(err); got != outcomeNeutral {
			t.Fatalf("classify(%v) = %d, want neutral", err, got)
		}
	}
}

func TestAttemptOpensAfterFailures(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 3, OpenFor: time.Second})

	for range 2 {
		finish(t, breaker, &pgconn.ConnectError{})
	}

	finish(t, breaker, nil)

	if isRefused(breaker) {
		t.Fatal("a success did not reset the failure count")
	}

	for range 3 {
		finish(t, breaker, &pgconn.ConnectError{})
	}

	if !isRefused(breaker) {
		t.Fatal("three failures did not open the breaker")
	}
}

func openBreaker(t *testing.T, breaker *Breaker, clock *testClock) {
	t.Helper()

	for range breaker.cfg.Failures {
		finish(t, breaker, &pgconn.ConnectError{})
	}

	clock.Advance(breaker.cfg.OpenFor)
}

func TestAttemptHalfOpenSingleProbe(t *testing.T) {
	for _, tc := range []struct {
		name     string
		probe    error
		isClosed bool
	}{
		{name: "failed probe reopens", probe: &pgconn.ConnectError{}},
		{name: "successful probe closes", isClosed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			breaker, clock := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
			openBreaker(t, breaker, clock)

			var (
				granted atomic.Int32
				probe   atomic.Pointer[Attempt]
				wg      sync.WaitGroup
			)

			for range 50 {
				wg.Go(func() {
					_, attempt, err := breaker.Attempt(context.Background())
					if err == nil {
						granted.Add(1)
						probe.Store(attempt)
					}
				})
			}

			wg.Wait()

			if granted.Load() != 1 {
				t.Fatalf("granted %d probes", granted.Load())
			}

			probe.Load().Done(tc.probe)

			if isRefused(breaker) == tc.isClosed {
				t.Fatalf("closed = %t, want %t", !isRefused(breaker), tc.isClosed)
			}
		})
	}
}

func TestAttemptNeutralKeepsState(t *testing.T) {
	breaker, clock := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
	openBreaker(t, breaker, clock)

	_, probe, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	probe.Done(errors.New("application error"))

	_, next, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatalf("no new probe after a neutral end: %v", err)
	}

	if !isRefused(breaker) {
		t.Fatal("a second probe was issued while the first one is held")
	}

	next.Done(nil)

	if isRefused(breaker) {
		t.Fatal("the new probe did not close the breaker")
	}
}

func TestAttemptLateDoneIgnored(t *testing.T) {
	breaker, clock := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
	openBreaker(t, breaker, clock)

	_, first, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Second)

	_, second, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatalf("an expired probe was not reissued: %v", err)
	}

	first.Done(nil)

	if !isRefused(breaker) {
		t.Fatal("a late Done from an expired probe closed the breaker")
	}

	second.Done(nil)

	if isRefused(breaker) {
		t.Fatal("the current probe did not close the breaker")
	}
}

func TestAttemptDoneTwice(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})

	_, attempt, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	attempt.Done(nil)
	attempt.Done(&pgconn.ConnectError{})

	if isRefused(breaker) {
		t.Fatal("a second Done was counted")
	}
}

func TestAttemptReleaseOnce(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})

	_, released, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	released.Release()
	released.Done(&pgconn.ConnectError{})

	if isRefused(breaker) {
		t.Fatal("Done after Release was counted")
	}

	_, done, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	done.Done(&pgconn.ConnectError{})
	done.Release()

	if !isRefused(breaker) {
		t.Fatal("Release after Done undid the failure")
	}
}

func TestAttemptNested(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 2, OpenFor: time.Second})

	ctx, outer, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	nestedCtx, nested, err := breaker.Attempt(ctx)
	if err != nil || nestedCtx != ctx {
		t.Fatalf("nested Attempt = %v, %v", nestedCtx, err)
	}

	nested.Done(&pgconn.ConnectError{})
	outer.Done(&pgconn.ConnectError{})

	if isRefused(breaker) {
		t.Fatal("the nested Done was counted")
	}

	finish(t, breaker, &pgconn.ConnectError{})

	if !isRefused(breaker) {
		t.Fatal("the outer Done was not counted")
	}
}

func TestAttemptCtxAfterDone(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})

	ctx, outer, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	outer.Done(&pgconn.ConnectError{})

	if _, _, err := breaker.Attempt(ctx); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("Attempt with a finished operation's ctx = %v, want ErrBreakerOpen", err)
	}
}

func TestAttemptNestedStaleProbe(t *testing.T) {
	breaker, clock := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
	openBreaker(t, breaker, clock)

	outerCtx, outer, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Second)

	_, current, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	_, nested, err := breaker.Attempt(outerCtx)
	if err != nil {
		t.Fatalf("nested Attempt under a stale probe = %v", err)
	}

	nested.Done(nil)
	outer.Done(nil)

	if !isRefused(breaker) {
		t.Fatal("a stale probe closed the breaker")
	}

	current.Done(nil)

	if isRefused(breaker) {
		t.Fatal("the current probe did not close the breaker")
	}
}

func TestNilBreakerGrants(t *testing.T) {
	var breaker *Breaker

	ctx, attempt, err := breaker.Attempt(context.Background())
	if err != nil || ctx == nil || attempt == nil {
		t.Fatalf("Attempt on a nil breaker = %v, %v, %v", ctx, attempt, err)
	}

	attempt.Done(&pgconn.ConnectError{})
	attempt.Release()
}

func TestClassifyCancelInsideDecodeBoundary(t *testing.T) {
	inner := errors.Join(pgx.ScanArgError{Err: context.Canceled}, io.EOF)

	for _, err := range []error{inner, fmt.Errorf("read: %w", inner)} {
		if got := classify(err); got != outcomeFailure {
			t.Fatalf("classify(%v) = %d, want failure: a cancellation inside a decoding error must not hide the transport error", err, got)
		}
	}
}
