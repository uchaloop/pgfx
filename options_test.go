package pgfx

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestBuildTracerOptIn verifies tracing is off by default and each seam
// (WithTracing, WithQueryMetrics) turns a tracer on.
func TestBuildTracerOptIn(t *testing.T) {
	if tr := makeOptions().buildTracer(); tr != nil {
		t.Fatalf("no options: expected a nil tracer (tracing is opt-in), got %T", tr)
	}
	if tr := makeOptions(WithTracing()).buildTracer(); tr == nil {
		t.Fatal("WithTracing: expected an otelpgx tracer")
	}
	if tr := makeOptions(WithQueryMetrics(func(QueryMetric) {})).buildTracer(); tr == nil {
		t.Fatal("WithQueryMetrics: expected a metrics tracer")
	}
	if tr := makeOptions(WithTracing(), WithQueryMetrics(func(QueryMetric) {})).buildTracer(); tr == nil {
		t.Fatal("WithTracing + WithQueryMetrics: expected a composed tracer")
	}
}

func TestWithTLSOverridesLibpqTLSAndRemovesFallbacks(t *testing.T) {
	customTLS := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: "database.internal",
	}
	cfg := Config{
		Host:     "database.internal",
		Database: "app",
	}

	poolCfg, err := cfg.poolConfig(makeOptions(WithTLS(customTLS)))
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if poolCfg.ConnConfig.TLSConfig != customTLS {
		t.Fatal("WithTLS did not install the provided TLS config")
	}
	if len(poolCfg.ConnConfig.Fallbacks) != 0 {
		t.Fatalf(
			"WithTLS kept %d libpq fallbacks, want none",
			len(poolCfg.ConnConfig.Fallbacks),
		)
	}
}

func TestWithTLSNilDisablesTLSAndRemovesFallbacks(t *testing.T) {
	cfg := Config{
		Host:     "database.internal",
		Database: "app",
		TLS:      TLSConfig{Mode: "allow"},
	}

	poolCfg, err := cfg.poolConfig(makeOptions(WithTLS(nil)))
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if poolCfg.ConnConfig.TLSConfig != nil {
		t.Fatal("WithTLS(nil) did not disable TLS")
	}
	if len(poolCfg.ConnConfig.Fallbacks) != 0 {
		t.Fatalf(
			"WithTLS(nil) kept %d libpq fallbacks, want none",
			len(poolCfg.ConnConfig.Fallbacks),
		)
	}
}

func TestWithBeforeConnect(t *testing.T) {
	called := false
	poolCfg := testPoolConfig(t, WithBeforeConnect(func(
		_ context.Context,
		cfg *pgx.ConnConfig,
	) error {
		called = true
		if cfg == nil || cfg.Host != "database.internal" {
			t.Errorf("BeforeConnect received an unexpected connection config: %#v", cfg)
		}

		return nil
	}))

	if poolCfg.BeforeConnect == nil {
		t.Fatal("WithBeforeConnect did not install the callback")
	}
	if err := poolCfg.BeforeConnect(context.Background(), poolCfg.ConnConfig); err != nil {
		t.Fatalf("BeforeConnect: %v", err)
	}
	if !called {
		t.Fatal("BeforeConnect callback was not called")
	}
}

func TestWithAfterConnect(t *testing.T) {
	called := false
	poolCfg := testPoolConfig(t, WithAfterConnect(func(
		context.Context,
		*pgx.Conn,
	) error {
		called = true

		return nil
	}))

	if poolCfg.AfterConnect == nil {
		t.Fatal("WithAfterConnect did not install the callback")
	}
	if err := poolCfg.AfterConnect(context.Background(), nil); err != nil {
		t.Fatalf("AfterConnect: %v", err)
	}
	if !called {
		t.Fatal("AfterConnect callback was not called")
	}
}

func TestWithRuntimeParam(t *testing.T) {
	poolCfg := testPoolConfig(
		t,
		WithRuntimeParam("search_path", "orders,public"),
	)
	if got := poolCfg.ConnConfig.RuntimeParams["search_path"]; got != "orders,public" {
		t.Fatalf("search_path = %q, want orders,public", got)
	}

	emptyPoolCfg := &pgxpool.Config{ConnConfig: &pgx.ConnConfig{}}
	opts := makeOptions(WithRuntimeParam("application_name", "orders"))
	for _, apply := range opts.poolOptions {
		apply(emptyPoolCfg)
	}
	if got := emptyPoolCfg.ConnConfig.RuntimeParams["application_name"]; got != "orders" {
		t.Fatalf("application_name = %q, want orders", got)
	}
}

func TestWithDefaultQueryExecMode(t *testing.T) {
	poolCfg := testPoolConfig(
		t,
		WithDefaultQueryExecMode(pgx.QueryExecModeSimpleProtocol),
	)
	if got := poolCfg.ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeSimpleProtocol {
		t.Fatalf("DefaultQueryExecMode = %v, want simple protocol", got)
	}
}

func TestWithStatementCacheCapacity(t *testing.T) {
	poolCfg := testPoolConfig(t, WithStatementCacheCapacity(128))
	if got := poolCfg.ConnConfig.StatementCacheCapacity; got != 128 {
		t.Fatalf("StatementCacheCapacity = %d, want 128", got)
	}
}

func TestWithTracer(t *testing.T) {
	custom := &metricsTracer{fn: func(QueryMetric) {}}
	got := makeOptions(WithTracer(custom)).buildTracer()
	if got != custom {
		t.Fatalf("buildTracer = %T, want the custom tracer", got)
	}
}

func TestWithSQLInQueryMetrics(t *testing.T) {
	var got QueryMetric
	tracer := makeOptions(
		WithQueryMetrics(func(metric QueryMetric) { got = metric }),
		WithSQLInQueryMetrics(true),
	).buildTracer()

	ctx := tracer.TraceQueryStart(
		context.Background(),
		nil,
		pgx.TraceQueryStartData{SQL: "select 1"},
	)
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if got.SQL != "select 1" {
		t.Fatalf("QueryMetric.SQL = %q, want select 1", got.SQL)
	}
}

func testPoolConfig(t *testing.T, opts ...Option) *pgxpool.Config {
	t.Helper()

	cfg := Config{
		Host:     "database.internal",
		Database: "app",
		TLS:      TLSConfig{Mode: "disable"},
	}
	poolCfg, err := cfg.poolConfig(makeOptions(opts...))
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	return poolCfg
}
