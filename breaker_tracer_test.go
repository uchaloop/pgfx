package pgfx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errStatementTimeout = &pgconn.PgError{Code: "57014"}

func traceQuery(ctx context.Context, tracer pgx.QueryTracer, err error) {
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: err})
}

func TestStateTracerAcquireSuccessNeutral(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 2, OpenFor: time.Second})
	tracer := stateTracer{state: &connState{breaker: breaker}}

	for range 2 {
		ctx := tracer.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
		tracer.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})

		traceQuery(context.Background(), tracer, errStatementTimeout)
	}

	if !isRefused(breaker) {
		t.Fatal("successful acquires reset the failure count")
	}
}

func TestStateTracerSkipsAttemptEvents(t *testing.T) {
	own, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
	other, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})

	ctx, attempt, err := own.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.Release()

	traceQuery(ctx, stateTracer{state: &connState{breaker: own}}, errStatementTimeout)
	traceQuery(ctx, stateTracer{state: &connState{breaker: other}}, errStatementTimeout)

	if isRefused(own) {
		t.Fatal("an event inside the breaker's own attempt was counted")
	}

	if !isRefused(other) {
		t.Fatal("an event of another breaker was suppressed")
	}
}

func TestStateTracerBatchOnce(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 2, OpenFor: time.Second})
	tracer := stateTracer{state: &connState{breaker: breaker}}

	ctx := tracer.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{})
	tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: errStatementTimeout})
	tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: errStatementTimeout})

	if isRefused(breaker) {
		t.Fatal("one batch was counted twice")
	}

	traceQuery(context.Background(), tracer, errStatementTimeout)

	if !isRefused(breaker) {
		t.Fatal("the batch failure was not counted")
	}
}

type recordingTracer struct{ starts, ends int }

func (r *recordingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	r.starts++

	return ctx
}

func (r *recordingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
	r.ends++
}

func TestStateTracerKeepsUserTracer(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 2, OpenFor: time.Second})
	recorder := &recordingTracer{}

	opts := makeOptions(WithTracer(recorder))
	opts.state = &connState{breaker: breaker}

	tracer := opts.buildTracer()
	traceQuery(context.Background(), tracer, errStatementTimeout)

	if recorder.starts != 1 || recorder.ends != 1 {
		t.Fatalf("user tracer saw %d starts and %d ends", recorder.starts, recorder.ends)
	}

	acquire, isOK := tracer.(pgxpool.AcquireTracer)
	if !isOK {
		t.Fatal("the composed tracer does not trace pool acquisition")
	}

	ctx := acquire.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	acquire.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Err: &pgconn.ConnectError{}})

	if !isRefused(breaker) {
		t.Fatal("the query and acquire failures were not both counted")
	}
}

func TestStateTracerWithoutBreaker(t *testing.T) {
	tracer := stateTracer{state: &connState{}}
	ctx := context.Background()

	allocs := testing.AllocsPerRun(100, func() {
		traceQuery(ctx, tracer, errStatementTimeout)
	})
	if allocs != 0 {
		t.Fatalf("allocations without a breaker = %v", allocs)
	}

	if makeOptions().buildTracer() != nil {
		t.Fatal("a connection without breaker, tracing or metrics installs a tracer")
	}
}

func TestStateTracerStaleGeneration(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Hour})
	tracer := stateTracer{state: &connState{breaker: breaker}}

	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{})

	finish(t, breaker, &pgconn.ConnectError{})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if !isRefused(breaker) {
		t.Fatal("a query from before the opening closed the breaker")
	}
}

func TestStateTracerSuppressionFixedAtStart(t *testing.T) {
	breaker, _ := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
	tracer := stateTracer{state: &connState{breaker: breaker}}

	ctx, attempt, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	queryCtx := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{})
	attempt.Done(nil)
	tracer.TraceQueryEnd(queryCtx, nil, pgx.TraceQueryEndData{Err: errStatementTimeout})

	if isRefused(breaker) {
		t.Fatal("a query started inside the attempt was counted after Done")
	}

	traceQuery(ctx, tracer, errStatementTimeout)

	if !isRefused(breaker) {
		t.Fatal("a query started with the old ctx after Done was not counted")
	}
}

func TestStateTracerHalfOpenIgnored(t *testing.T) {
	breaker, clock := makeTestBreaker(t, BreakerConfig{Failures: 1, OpenFor: time.Second})
	tracer := stateTracer{state: &connState{breaker: breaker}}
	openBreaker(t, breaker, clock)

	traceQuery(context.Background(), tracer, nil)

	_, probe, err := breaker.Attempt(context.Background())
	if err != nil {
		t.Fatalf("no probe in half-open: %v", err)
	}
	defer probe.Release()

	if _, _, err := breaker.Attempt(context.Background()); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("a tracer success closed a half-open breaker: %v", err)
	}
}
