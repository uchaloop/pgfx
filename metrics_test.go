package pgfx

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestQueryName(t *testing.T) {
	ctx := WithQueryName(context.Background(), "orders.get_by_id")
	if got := QueryName(ctx); got != "orders.get_by_id" {
		t.Fatalf("QueryName = %q, want orders.get_by_id", got)
	}
	if got := QueryName(context.Background()); got != "" {
		t.Fatalf("QueryName without a name = %q, want empty", got)
	}
}

func TestMetricsTracerOmitsSQLByDefault(t *testing.T) {
	var got []QueryMetric
	tracer := metricsTracer{fn: func(metric QueryMetric) {
		got = append(got, metric)
	}}

	ctx := WithQueryName(context.Background(), "orders.list")
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "select secret"})
	wantErr := errors.New("query failed")
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		CommandTag: pgconn.NewCommandTag("SELECT 3"),
		Err:        wantErr,
	})

	if len(got) != 1 {
		t.Fatalf("callback count = %d, want 1", len(got))
	}
	if got[0].Kind != KindQuery {
		t.Errorf("Kind = %v, want query", got[0].Kind)
	}
	if got[0].Name != "orders.list" {
		t.Errorf("Name = %q, want orders.list", got[0].Name)
	}
	if got[0].SQL != "" {
		t.Errorf("SQL = %q, want empty", got[0].SQL)
	}
	if got[0].RowsAffected != 3 {
		t.Errorf("RowsAffected = %d, want 3", got[0].RowsAffected)
	}
	if !errors.Is(got[0].Err, wantErr) {
		t.Errorf("Err = %v, want %v", got[0].Err, wantErr)
	}
	if got[0].Duration < 0 {
		t.Errorf("Duration = %v, want non-negative", got[0].Duration)
	}
}

func TestMetricsTracerIncludesSQLWhenEnabled(t *testing.T) {
	var got QueryMetric
	tracer := metricsTracer{
		fn:         func(metric QueryMetric) { got = metric },
		includeSQL: true,
	}

	ctx := tracer.TraceQueryStart(
		context.Background(),
		nil,
		pgx.TraceQueryStartData{SQL: "select 1"},
	)
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if got.SQL != "select 1" {
		t.Fatalf("SQL = %q, want select 1", got.SQL)
	}
}

func TestMetricsTracerIgnoresEndWithoutStart(t *testing.T) {
	calls := 0
	tracer := metricsTracer{fn: func(QueryMetric) { calls++ }}
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})
	if calls != 0 {
		t.Fatalf("callback count = %d, want 0", calls)
	}
}

func batchOf(statements ...string) *pgx.Batch {
	batch := &pgx.Batch{}
	for _, statement := range statements {
		batch.Queue(statement)
	}

	return batch
}

func TestQueryKindLabels(t *testing.T) {
	for kind, want := range map[QueryKind]string{
		KindQuery:    "query",
		KindBatch:    "batch",
		KindCopyFrom: "copy_from",
		QueryKind(9): "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("QueryKind(%d) = %q, want %q", kind, got, want)
		}
	}
}

func TestMetricsTracerBatchIsOneObservation(t *testing.T) {
	var got []QueryMetric
	tracer := metricsTracer{fn: func(metric QueryMetric) { got = append(got, metric) }}

	ctx := WithQueryName(context.Background(), "orders.sync")
	ctx = tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{
		Batch: batchOf("select 1", "insert into orders values (1)"),
	})

	tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{
		CommandTag: pgconn.NewCommandTag("SELECT 2"),
	})
	tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{
		CommandTag: pgconn.NewCommandTag("INSERT 0 3"),
	})
	tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})

	// One metric for the lifecycle, not a latency for each statement.
	if len(got) != 1 {
		t.Fatalf("callback count = %d, want 1", len(got))
	}
	if got[0].Kind != KindBatch {
		t.Errorf("Kind = %v, want batch", got[0].Kind)
	}
	if got[0].Name != "orders.sync" {
		t.Errorf("Name = %q, want orders.sync", got[0].Name)
	}
	if got[0].RowsAffected != 5 {
		t.Errorf("RowsAffected = %d, want 5 (summed over the batch)", got[0].RowsAffected)
	}
	if got[0].SQL != "" {
		t.Errorf("SQL = %q, want empty", got[0].SQL)
	}
	if got[0].Err != nil {
		t.Errorf("Err = %v, want nil", got[0].Err)
	}
}

func TestMetricsTracerBatchKeepsStatementError(t *testing.T) {
	var got QueryMetric
	tracer := metricsTracer{fn: func(metric QueryMetric) { got = metric }}

	wantErr := errors.New("statement failed")
	later := errors.New("another statement failed")

	ctx := tracer.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{
		Batch: batchOf("select 1"),
	})
	tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{Err: wantErr})
	tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{Err: later})
	tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})

	// The batch itself ended clean, so the first statement error is what there is
	// to report.
	if !errors.Is(got.Err, wantErr) {
		t.Fatalf("Err = %v, want %v", got.Err, wantErr)
	}
}

func TestMetricsTracerBatchEndErrorWins(t *testing.T) {
	var got QueryMetric
	tracer := metricsTracer{fn: func(metric QueryMetric) { got = metric }}

	statementErr := errors.New("statement failed")
	wantErr := errors.New("batch failed")

	ctx := tracer.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{
		Batch: batchOf("select 1"),
	})
	tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{Err: statementErr})
	tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: wantErr})

	if !errors.Is(got.Err, wantErr) {
		t.Fatalf("Err = %v, want %v", got.Err, wantErr)
	}
}

func TestMetricsTracerBatchIncludesSQLWhenEnabled(t *testing.T) {
	var got QueryMetric
	tracer := metricsTracer{
		fn:         func(metric QueryMetric) { got = metric },
		includeSQL: true,
	}

	batch := batchOf("select 1", "select 2")
	ctx := tracer.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{Batch: batch})
	batch.QueuedQueries[0].SQL = "rewritten"
	tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})

	if got.SQL != "select 1; select 2" {
		t.Fatalf("SQL = %q, want the queued statements joined", got.SQL)
	}
}

func TestMetricsTracerBatchEndIsIdempotent(t *testing.T) {
	for name, earlyErr := range map[string]error{"success": nil, "early_error": errors.New("prepare failed")} {
		t.Run(name, func(t *testing.T) {
			var got []QueryMetric
			tracer := metricsTracer{fn: func(m QueryMetric) { got = append(got, m) }}
			ctx := tracer.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{})
			start := ctx.Value(batchStartKey{}).(*batchStart)
			start.at = time.Now().Add(-time.Second)
			tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: earlyErr})
			tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{CommandTag: pgconn.NewCommandTag("SELECT 9")})
			tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: errors.New("later error")})
			if len(got) != 1 {
				t.Fatalf("callback count = %d, want 1", len(got))
			}
			if got[0].Err != earlyErr || got[0].RowsAffected != 0 || got[0].Duration < time.Second {
				t.Fatalf("unexpected metric: %+v", got[0])
			}
			if start.rows != 0 {
				t.Fatal("late query callback mutated ended batch")
			}
		})
	}
}

func TestMetricsTracerConcurrentBatches(t *testing.T) {
	var mu sync.Mutex
	var got []QueryMetric
	tracer := metricsTracer{fn: func(m QueryMetric) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m)
	}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			ctx := tracer.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{})
			tracer.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{CommandTag: pgconn.NewCommandTag("SELECT 2")})
			tracer.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})
		})
	}
	wg.Wait()
	if len(got) != 20 {
		t.Fatalf("callback count = %d, want 20", len(got))
	}
	for _, m := range got {
		if m.Kind != KindBatch || m.RowsAffected != 2 {
			t.Fatalf("state leaked across batches: %+v", m)
		}
	}
}

func TestMetricsTracerCopyFrom(t *testing.T) {
	var got QueryMetric
	tracer := metricsTracer{
		fn:         func(metric QueryMetric) { got = metric },
		includeSQL: true,
	}

	wantErr := errors.New("copy failed")

	ctx := WithQueryName(context.Background(), "orders.load")
	ctx = tracer.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{
		TableName:   pgx.Identifier{"orders"},
		ColumnNames: []string{"id"},
	})
	tracer.TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{
		CommandTag: pgconn.NewCommandTag("COPY 7"),
		Err:        wantErr,
	})

	if got.Kind != KindCopyFrom {
		t.Errorf("Kind = %v, want copy_from", got.Kind)
	}
	if got.Name != "orders.load" {
		t.Errorf("Name = %q, want orders.load", got.Name)
	}
	if got.RowsAffected != 7 {
		t.Errorf("RowsAffected = %d, want 7", got.RowsAffected)
	}
	if !errors.Is(got.Err, wantErr) {
		t.Errorf("Err = %v, want %v", got.Err, wantErr)
	}
	// A table name is not a statement, so nothing lands in SQL even when the
	// option is on.
	if got.SQL != "" {
		t.Errorf("SQL = %q, want empty", got.SQL)
	}
}

func TestMetricsTracerIgnoresEndsWithoutStart(t *testing.T) {
	calls := 0
	tracer := metricsTracer{fn: func(QueryMetric) { calls++ }}

	tracer.TraceBatchQuery(context.Background(), nil, pgx.TraceBatchQueryData{})
	tracer.TraceBatchEnd(context.Background(), nil, pgx.TraceBatchEndData{})
	tracer.TraceCopyFromEnd(context.Background(), nil, pgx.TraceCopyFromEndData{})

	if calls != 0 {
		t.Fatalf("callback count = %d, want 0", calls)
	}
}

func TestMetricsTracerImplementsPgxTracers(t *testing.T) {
	var tracer any = metricsTracer{}

	if _, ok := tracer.(pgx.QueryTracer); !ok {
		t.Error("metricsTracer is not a pgx.QueryTracer")
	}
	// multitracer routes by these interfaces, and so does pgx when the metrics
	// tracer is the only one on the pool.
	if _, ok := tracer.(pgx.BatchTracer); !ok {
		t.Error("metricsTracer is not a pgx.BatchTracer")
	}
	if _, ok := tracer.(pgx.CopyFromTracer); !ok {
		t.Error("metricsTracer is not a pgx.CopyFromTracer")
	}
}

func TestMetricsTracerComposesWithSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, parent := provider.Tracer("pgfx-test").Start(context.Background(), "parent")
	defer parent.End()
	ctx = WithQueryName(ctx, "orders.sync")
	var got []QueryMetric
	tracer := makeOptions(
		WithTracing(otelpgx.WithTracerProvider(provider)),
		WithQueryMetrics(func(m QueryMetric) { got = append(got, m) }),
	).buildTracer()

	batchTracer := tracer.(pgx.BatchTracer)
	batchCtx := batchTracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{Batch: batchOf("select 1")})
	batchTracer.TraceBatchQuery(batchCtx, nil, pgx.TraceBatchQueryData{SQL: "select 1", CommandTag: pgconn.NewCommandTag("SELECT 1")})
	batchTracer.TraceBatchEnd(batchCtx, nil, pgx.TraceBatchEndData{})
	copyTracer := tracer.(pgx.CopyFromTracer)
	copyCtx := copyTracer.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"orders"}})
	copyTracer.TraceCopyFromEnd(copyCtx, nil, pgx.TraceCopyFromEndData{CommandTag: pgconn.NewCommandTag("COPY 2")})
	if len(got) != 2 || got[0].Kind != KindBatch || got[1].Kind != KindCopyFrom {
		t.Fatalf("composed metrics = %+v", got)
	}
	if got[0].Name != "orders.sync" || got[1].Name != "orders.sync" || got[0].RowsAffected != 1 || got[1].RowsAffected != 2 {
		t.Fatalf("context or row counts lost: %+v", got)
	}
	var batchSpan, copySpan bool
	for _, span := range recorder.Ended() {
		batchSpan = batchSpan || span.Name() == "batch start"
		copySpan = copySpan || span.Name() == `copy_from "orders"`
	}
	if !batchSpan || !copySpan {
		t.Fatalf("missing composed spans: batch=%t, copy=%t", batchSpan, copySpan)
	}
}
