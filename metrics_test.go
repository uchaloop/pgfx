package pgfx

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
