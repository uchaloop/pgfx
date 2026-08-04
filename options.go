package pgfx

import (
	"context"
	"crypto/tls"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
)

// options holds the runtime dependencies of a connection - everything that is
// not serializable configuration. They are supplied to Make via Option.
type options struct {
	tracing                  bool
	tracingOptions           []otelpgx.Option
	tracer                   pgx.QueryTracer
	queryMetrics             QueryMetricFunc
	includeSQLInQueryMetrics bool
	poolOptions              []func(*pgxpool.Config)
}

// Option configures the runtime behaviour of a connection built by Make.
type Option func(*options)

func makeOptions(opts ...Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// buildTracer assembles the pool's pgx.QueryTracer from whatever is enabled: the
// otelpgx span tracer (WithTracing), a custom tracer (WithTracer), and the
// metrics callback (WithQueryMetrics). Nothing enabled -> nil (no tracer). Two or
// more are composed with multitracer.
func (o *options) buildTracer() pgx.QueryTracer {
	var tracers []pgx.QueryTracer

	if o.tracing {
		tracers = append(tracers, otelpgx.NewTracer(o.tracingOptions...))
	}
	if o.tracer != nil {
		tracers = append(tracers, o.tracer)
	}
	if o.queryMetrics != nil {
		tracers = append(tracers, metricsTracer{
			fn:         o.queryMetrics,
			includeSQL: o.includeSQLInQueryMetrics,
		})
	}

	switch len(tracers) {
	case 0:
		return nil
	case 1:
		return tracers[0]
	default:
		return multitracer.New(tracers...)
	}
}

// WithTracing enables OpenTelemetry query spans via [otelpgx]. Tracing is off by
// default; this turns it on. Configure it with otelpgx's own options, e.g.
// WithTracing(otelpgx.WithTracerProvider(tp), otelpgx.WithTrimSQLInSpanName()).
//
// [otelpgx]: https://github.com/exaring/otelpgx
func WithTracing(opts ...otelpgx.Option) Option {
	return func(o *options) {
		o.tracing = true
		o.tracingOptions = append(o.tracingOptions, opts...)
	}
}

// WithTracer adds a custom pgx.QueryTracer (for example an AWS X-Ray tracer).
// It composes with WithTracing and WithQueryMetrics rather than replacing them.
func WithTracer(tracer pgx.QueryTracer) Option {
	return func(o *options) { o.tracer = tracer }
}

// WithQueryMetrics installs a second pgx.QueryTracer that reports a QueryMetric
// for every query (composed with the span tracer). nil disables it.
func WithQueryMetrics(fn QueryMetricFunc) Option {
	return func(o *options) { o.queryMetrics = fn }
}

// WithSQLInQueryMetrics includes the raw SQL in QueryMetric.SQL (default off).
// Enable only when statements are safe to expose.
func WithSQLInQueryMetrics(include bool) Option {
	return func(o *options) { o.includeSQLInQueryMetrics = include }
}

// WithBeforeConnect installs pgxpool.Config.BeforeConnect - invoked before
// establishing each new connection (e.g. to rotate a password).
func WithBeforeConnect(fn func(context.Context, *pgx.ConnConfig) error) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.BeforeConnect = fn })
}

// WithAfterConnect installs pgxpool.Config.AfterConnect - the usual place for
// custom pgx type registration.
func WithAfterConnect(fn func(context.Context, *pgx.Conn) error) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.AfterConnect = fn })
}

// WithTLS sets the connection's *tls.Config directly, overriding whatever the
// file-based Config.TLS produced (sslmode and the cert/key/root-cert paths). Use
// it when certificates are not files on disk - loaded from a secret manager,
// held in memory, or rotated at runtime: the application builds the *tls.Config
// and passes it here. It also removes libpq TLS/plaintext fallbacks so every
// connection attempt uses exactly this configuration. A nil value disables TLS.
func WithTLS(tlsConfig *tls.Config) Option {
	return withConnConfig(func(cfg *pgx.ConnConfig) {
		cfg.TLSConfig = tlsConfig
		cfg.Fallbacks = nil
	})
}

// WithDefaultQueryExecMode sets the default pgx.QueryExecMode.
func WithDefaultQueryExecMode(mode pgx.QueryExecMode) Option {
	return withConnConfig(func(cfg *pgx.ConnConfig) { cfg.DefaultQueryExecMode = mode })
}

// WithStatementCacheCapacity sets the statement-cache size per connection.
func WithStatementCacheCapacity(capacity int) Option {
	return withConnConfig(func(cfg *pgx.ConnConfig) { cfg.StatementCacheCapacity = capacity })
}

// WithRuntimeParam adds a single pgx.ConnConfig.RuntimeParams value (SET key =
// value on connect).
func WithRuntimeParam(key, value string) Option {
	return withConnConfig(func(cfg *pgx.ConnConfig) {
		if cfg.RuntimeParams == nil {
			cfg.RuntimeParams = make(map[string]string)
		}

		cfg.RuntimeParams[key] = value
	})
}

func withConnConfig(mutate func(*pgx.ConnConfig)) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { mutate(cfg.ConnConfig) })
}

func withPoolConfig(mutate func(*pgxpool.Config)) Option {
	return func(o *options) {
		o.poolOptions = append(o.poolOptions, mutate)
	}
}
