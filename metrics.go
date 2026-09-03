package pgfx

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryKind is the operation a QueryMetric describes.
//
// Use it as a bounded metric label to separate queries, batches and bulk loads.
type QueryKind uint8

const (
	// KindQuery is a single query - Query, QueryRow or Exec. It is the zero
	// value, so a metric that says nothing about its kind is a query.
	KindQuery QueryKind = iota
	// KindBatch is one SendBatch operation, including reading its results.
	KindBatch
	// KindCopyFrom is one CopyFrom.
	KindCopyFrom
)

// String returns the kind as a metric label value.
func (k QueryKind) String() string {
	switch k {
	case KindQuery:
		return "query"
	case KindBatch:
		return "batch"
	case KindCopyFrom:
		return "copy_from"
	default:
		return "unknown"
	}
}

// QueryMetric is one observed operation, passed to a QueryMetricFunc.
type QueryMetric struct {
	// Kind is what was observed: a query, a batch or a copy. Use it as a metric
	// label - it is bounded - to keep the three apart.
	Kind QueryKind
	// Name is the bounded label from WithQueryName ("" if unset) - use it as the
	// metric label, never SQL (raw statements have unbounded cardinality).
	Name string
	// SQL is the raw statement when explicitly enabled with WithSQLInQueryMetrics;
	// otherwise it is empty. For a batch it is every queued statement joined with
	// "; " before query rewriting, including statements that may not execute.
	// For a copy it is empty. Arguments are not interpolated. Do NOT use it as a
	// metric label, and scrub it before recording; batches can produce long text.
	SQL string
	// Duration is elapsed time between pgx start/end hooks, not server execution
	// or network latency. It excludes pool acquisition and includes preparation,
	// result consumption (including delays before BatchResults.Close), and COPY
	// source production as applicable. Always close batch results.
	Duration time.Duration
	// RowsAffected is the number of rows affected or returned as reported by pgx,
	// summed over observed batch command tags. It can be partial on error and
	// does not indicate committed changes: the transaction may roll back.
	RowsAffected int64
	// Err is non-nil when the operation failed. For a batch it is the error the
	// batch ended with, or the first statement error when the batch itself
	// reported none.
	Err error
}

// QueryMetricFunc receives one QueryMetric per completed pgx trace lifecycle.
// A batch produces at most one metric, not one per statement. The callback must
// be concurrency-safe, cheap and non-blocking; different connections call it
// concurrently. The package ships no metrics dependency.
//
// Coverage follows pgx hooks: pool acquisition failures and empty batches are
// not observed. In pgx v5.10, some early CopyFrom failures (such as statement
// description errors) omit the end hook and therefore produce no metric.
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

type batchStartKey struct{}

// batchStart accumulates one batch while its results are read. The three batch
// callbacks run in sequence for a batch, and pgx.BatchResults is not
// safe for concurrent use either, so the counters need no locking.
type batchStart struct {
	at    time.Time
	sql   string
	rows  int64
	err   error
	ended bool
}

type copyStartKey struct{}

// metricsTracer reports query, batch and copy lifecycles exposed by pgx hooks.
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
		Kind:         KindQuery,
		Name:         QueryName(ctx),
		SQL:          start.sql,
		Duration:     time.Since(start.at),
		RowsAffected: data.CommandTag.RowsAffected(),
		Err:          data.Err,
	})
}

func (t metricsTracer) TraceBatchStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceBatchStartData,
) context.Context {
	start := &batchStart{at: time.Now()}
	if t.includeSQL {
		start.sql = batchSQL(data.Batch)
	}

	return context.WithValue(ctx, batchStartKey{}, start)
}

// TraceBatchQuery observes statement results as pgx reads them. Early errors
// can prevent callbacks for some queued statements.
// It accumulates rather than reports: the statements were sent together, so the
// time spent reading one result is not that statement's duration.
func (t metricsTracer) TraceBatchQuery(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceBatchQueryData,
) {
	start, ok := ctx.Value(batchStartKey{}).(*batchStart)
	if !ok || start.ended {
		return
	}

	start.rows += data.CommandTag.RowsAffected()

	if start.err == nil {
		start.err = data.Err
	}
}

func (t metricsTracer) TraceBatchEnd(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceBatchEndData,
) {
	start, ok := ctx.Value(batchStartKey{}).(*batchStart)
	if !ok || start.ended {
		return
	}
	// pgx can end a batch on a SendBatch early error and again on Close.
	// Mark it before invoking user code, including in case that code panics.
	start.ended = true

	err := data.Err
	if err == nil {
		// A statement can fail on a batch that ends without an error of its own.
		err = start.err
	}

	t.fn(QueryMetric{
		Kind:         KindBatch,
		Name:         QueryName(ctx),
		SQL:          start.sql,
		Duration:     time.Since(start.at),
		RowsAffected: start.rows,
		Err:          err,
	})
}

func (t metricsTracer) TraceCopyFromStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceCopyFromStartData,
) context.Context {
	return context.WithValue(ctx, copyStartKey{}, time.Now())
}

func (t metricsTracer) TraceCopyFromEnd(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceCopyFromEndData,
) {
	at, ok := ctx.Value(copyStartKey{}).(time.Time)
	if !ok {
		return
	}

	t.fn(QueryMetric{
		Kind:         KindCopyFrom,
		Name:         QueryName(ctx),
		Duration:     time.Since(at),
		RowsAffected: data.CommandTag.RowsAffected(),
		Err:          data.Err,
	})
}

// batchSQL joins the statements a batch queued, in the order they were queued.
func batchSQL(batch *pgx.Batch) string {
	if batch == nil {
		return ""
	}

	statements := make([]string, 0, len(batch.QueuedQueries))
	for _, queued := range batch.QueuedQueries {
		statements = append(statements, queued.SQL)
	}

	return strings.Join(statements, "; ")
}
