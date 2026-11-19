package pgfx

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestBuildTracerOptIn verifies tracing is off by default and each seam
// (WithTrace, WithQueryMetrics) turns a tracer on.
func TestBuildTracerOptIn(t *testing.T) {
	if tr := makeOptions().buildTracer(); tr != nil {
		t.Fatalf("no options: expected a nil tracer (tracing is opt-in), got %T", tr)
	}

	if tr := makeOptions(WithTrace()).buildTracer(); tr == nil {
		t.Fatal("WithTrace: expected an otelpgx tracer")
	}

	if tr := makeOptions(WithQueryMetrics(func(QueryMetric) {})).buildTracer(); tr == nil {
		t.Fatal("WithQueryMetrics: expected a metrics tracer")
	}

	if tr := makeOptions(WithTrace(), WithQueryMetrics(func(QueryMetric) {})).buildTracer(); tr == nil {
		t.Fatal("WithTrace + WithQueryMetrics: expected a composed tracer")
	}
}

func TestWithBeforeConnect(t *testing.T) {
	isCalled := false
	poolCfg := makeTestPoolConfig(t, WithBeforeConnect(func(
		_ context.Context,
		cfg *pgx.ConnConfig,
	) error {
		isCalled = true
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

	if !isCalled {
		t.Fatal("BeforeConnect callback was not called")
	}
}

func TestWithAfterConnect(t *testing.T) {
	isCalled := false
	poolCfg := makeTestPoolConfig(t, WithAfterConnect(func(
		context.Context,
		*pgx.Conn,
	) error {
		isCalled = true

		return nil
	}))

	if poolCfg.AfterConnect == nil {
		t.Fatal("WithAfterConnect did not install the callback")
	}

	if err := poolCfg.AfterConnect(context.Background(), nil); err != nil {
		t.Fatalf("AfterConnect: %v", err)
	}

	if !isCalled {
		t.Fatal("AfterConnect callback was not called")
	}
}

func TestWithRuntimeParam(t *testing.T) {
	poolCfg := makeTestPoolConfig(
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
	poolCfg := makeTestPoolConfig(
		t,
		WithDefaultQueryExecMode(pgx.QueryExecModeSimpleProtocol),
	)
	if got := poolCfg.ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeSimpleProtocol {
		t.Fatalf("DefaultQueryExecMode = %v, want simple protocol", got)
	}
}

func TestWithStatementCacheCapacity(t *testing.T) {
	poolCfg := makeTestPoolConfig(t, WithStatementCacheCapacity(128))
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

func makeTestPoolConfig(t *testing.T, opts ...Option) *pgxpool.Config {
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

func TestTLSOverrideSkipsFileConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mode           string
		isDisabled     bool
		isInvalidFiles bool
	}{
		{name: "default mode"},
		{name: "allow disabled", mode: "allow", isDisabled: true},
		{name: "invalid custom", mode: "invalid", isInvalidFiles: true},
		{name: "invalid disabled", mode: "invalid", isDisabled: true, isInvalidFiles: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				Host:     "db.internal",
				Database: "app",
				TLS:      TLSConfig{Mode: tc.mode},
			}

			if tc.isInvalidFiles {
				absentPath := filepath.Join(t.TempDir(), "missing.pem")
				t.Setenv("PGSSLMODE", "invalid")
				t.Setenv("PGSSLROOTCERT", absentPath)
				t.Setenv("PGSSLCERT", absentPath)
				t.Setenv("PGSSLKEY", absentPath)
				cfg.TLS.RootCert = absentPath
				cfg.TLS.Cert = absentPath
				cfg.TLS.Key = absentPath
			}

			var custom *tls.Config

			if !tc.isDisabled {
				custom = &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "db.internal"}
			}

			got, err := cfg.poolConfig(makeOptions(WithTLS(custom)))
			if err != nil {
				t.Fatal(err)
			}

			if got.ConnConfig.TLSConfig != custom || len(got.ConnConfig.Fallbacks) != 0 {
				t.Fatal("TLS override or fallback policy lost")
			}
		})
	}
}

func TestTLSOverrideLastOptionWins(t *testing.T) {
	cfg := makeValidConfig()
	custom := &tls.Config{ServerName: "db.internal", MinVersion: tls.VersionTLS12}

	for _, tc := range []struct {
		opts []Option
		want *tls.Config
	}{
		{opts: []Option{WithTLS(custom), WithTLS(nil)}, want: nil},
		{opts: []Option{WithTLS(nil), WithTLS(custom)}, want: custom},
	} {
		got, err := cfg.poolConfig(makeOptions(tc.opts...))
		if err != nil {
			t.Fatal(err)
		}

		expected := tc.want
		if got.ConnConfig.TLSConfig != expected {
			t.Fatal("last TLS option lost")
		}

		if len(got.ConnConfig.Fallbacks) != 0 {
			t.Fatal("fallback retained")
		}
	}
}

func TestTLSWithoutOverrideStillValidatesFiles(t *testing.T) {
	cfg := makeValidConfig()
	cfg.TLS = TLSConfig{Mode: "verify-full", RootCert: filepath.Join(t.TempDir(), "missing.pem")}
	if _, err := cfg.poolConfig(makeOptions()); err == nil {
		t.Fatal("invalid root certificate accepted")
	}
}
