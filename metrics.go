package pgfx

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryMetric is one observed query, passed to a QueryMetricFunc.
type QueryMetric struct {
	// Name is the bounded label from WithQueryName ("" if unset) - use it as the
	// metric label, never SQL (raw statements have unbounded cardinality).
	Name string
	// SQL is the raw statement when explicitly enabled with WithSQLInQueryMetrics;
	// otherwise it is empty. Do NOT use it as a metric label, and scrub it before
	// recording.
	SQL string
	// Duration is the wall time from query start to end.
	Duration time.Duration
	// RowsAffected is the number of rows affected or returned as reported by pgx.
	RowsAffected int64
	// Err is non-nil when the query failed.
	Err error
}

// QueryMetricFunc receives one QueryMetric per query. It runs on the query hot
// path, so it must be cheap and non-blocking. The package ships no metrics
// dependency - this callback is the seam where you plug one in.
type QueryMetricFunc func(QueryMetric)

type queryNameKey struct{}

// WithQueryName tags ctx with a bounded query name used as QueryMetric.Name. Set
// it per call site (e.g. "orders.get_by_id"); keep the set of names small.
func WithQueryName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, queryNameKey{}, name)
}

// QueryName returns the name set by WithQueryName, or "" if none.
func QueryName(ctx context.Context) string {
	name, _ := ctx.Value(queryNameKey{}).(string)

	return name
}

type queryStartKey struct{}

type queryStart struct {
	at  time.Time
	sql string
}

// metricsTracer is a pgx.QueryTracer that times every query and reports it to
// fn. Composed with the span tracer on the pool, it observes all queries.
type metricsTracer struct {
	fn         QueryMetricFunc
	includeSQL bool
}

func (t metricsTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	start := queryStart{at: time.Now()}
	if t.includeSQL {
		start.sql = data.SQL
	}

	return context.WithValue(ctx, queryStartKey{}, start)
}

func (t metricsTracer) TraceQueryEnd(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryEndData,
) {
	start, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}

	t.fn(QueryMetric{
		Name:         QueryName(ctx),
		SQL:          start.sql,
		Duration:     time.Since(start.at),
		RowsAffected: data.CommandTag.RowsAffected(),
		Err:          data.Err,
	})
}
