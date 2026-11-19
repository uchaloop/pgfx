package pgfx

import (
	"context"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stateTracer reports the outcome of every pgx operation that does not run
// inside an Attempt of the connection's breaker. It is installed only for
// connections with a breaker.
type stateTracer struct {
	state *connState
}

// traceKey stores the decision a tracer took when an operation started.
type traceKey struct {
	breaker *Breaker
}

// traceDecision is fixed at the start of an operation, so an Attempt that ends
// while the operation still runs does not change how it is counted.
type traceDecision struct {
	generation   uint64
	isSuppressed bool
	isEnded      atomic.Bool
}

func (t stateTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return t.start(ctx)
}

func (t stateTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	t.end(ctx, data.Err, false)
}

func (t stateTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return t.start(ctx)
}

func (stateTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

// TraceBatchEnd counts once: pgx calls it after an early SendBatch error and
// again from BatchResults.Close.
func (t stateTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	t.end(ctx, data.Err, false)
}

func (t stateTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	return t.start(ctx)
}

func (t stateTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	t.end(ctx, data.Err, false)
}

func (t stateTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return t.start(ctx)
}

// TraceAcquireEnd counts only failures: a successful acquisition says nothing
// about the operation that follows.
func (t stateTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	t.end(ctx, data.Err, true)
}

func (t stateTracer) start(ctx context.Context) context.Context {
	breaker := t.state.breaker
	if breaker == nil {
		return ctx
	}

	decision := &traceDecision{isSuppressed: breaker.isSuppressed(ctx)}
	if !decision.isSuppressed {
		decision.generation = breaker.currentGeneration()
	}

	return context.WithValue(ctx, traceKey{breaker: breaker}, decision)
}

func (t stateTracer) end(ctx context.Context, err error, isFailureOnly bool) {
	breaker := t.state.breaker
	if breaker == nil {
		return
	}

	decision, isOK := ctx.Value(traceKey{breaker: breaker}).(*traceDecision)
	if !isOK || decision.isSuppressed || !decision.isEnded.CompareAndSwap(false, true) {
		return
	}

	result := classify(err)
	if result == outcomeNeutral || (isFailureOnly && result != outcomeFailure) {
		return
	}

	breaker.record(decision.generation, result)
}
