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
// not serializable configuration. They are supplied through Option.
type options struct {
	healthSQLStates     []string
	isTLSOverride       bool
	tlsConfig           *tls.Config
	isTraceEnabled      bool
	traceOptions        []otelpgx.Option
	tracer              pgx.QueryTracer
	queryMetrics        QueryMetricFunc
	isSQLInQueryMetrics bool
	poolOptions         []func(*pgxpool.Config)
}

// Option configures a connection built by MakeDB, MakeModule or MakeModuleFor.
// Pool callbacks may run concurrently on different connections. A callback
// borrows its connection only until it returns; do not retain it or re-enter
// the same pool. Bound I/O and honor contexts when supplied.
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
// otelpgx span tracer (WithTrace), a custom tracer (WithTracer), and the
// metrics callback (WithQueryMetrics). Nothing enabled -> nil (no tracer). Two or
// more are composed with multitracer.
func (o *options) buildTracer() pgx.QueryTracer {
	var tracers []pgx.QueryTracer

	if o.isTraceEnabled {
		tracers = append(tracers, otelpgx.NewTracer(o.traceOptions...))
	}

	if o.tracer != nil {
		tracers = append(tracers, o.tracer)
	}

	if o.queryMetrics != nil {
		tracers = append(tracers, metricsTracer{
			fn:            o.queryMetrics,
			isSQLIncluded: o.isSQLInQueryMetrics,
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

// WithTrace enables OpenTelemetry query spans via [otelpgx]. Tracing is off by
// default; this turns it on. Configure it with otelpgx's own options, e.g.
// WithTrace(otelpgx.WithTracerProvider(tp), otelpgx.WithTrimSQLInSpanName()).
//
// [otelpgx]: https://github.com/exaring/otelpgx
func WithTrace(opts ...otelpgx.Option) Option {
	return func(o *options) {
		o.isTraceEnabled = true
		o.traceOptions = append(o.traceOptions, opts...)
	}
}

// WithTracer adds a custom pgx.QueryTracer (for example an AWS X-Ray tracer).
// It composes with WithTrace and WithQueryMetrics rather than replacing them.
func WithTracer(tracer pgx.QueryTracer) Option {
	return func(o *options) { o.tracer = tracer }
}

// WithQueryMetrics reports query, batch and COPY metrics alongside any other
// tracers. See QueryMetricFunc for callback requirements and coverage limits.
// nil disables it.
func WithQueryMetrics(fn QueryMetricFunc) Option {
	return func(o *options) { o.queryMetrics = fn }
}

// WithSQLInQueryMetrics includes the raw SQL in QueryMetric.SQL (default off).
// Enable only when statements are safe to expose.
func WithSQLInQueryMetrics(isIncluded bool) Option {
	return func(o *options) { o.isSQLInQueryMetrics = isIncluded }
}

// WithBeforeConnect installs pgxpool.Config.BeforeConnect - invoked before
// establishing each new connection (e.g. to rotate a password).
func WithBeforeConnect(fn func(context.Context, *pgx.ConnConfig) error) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.BeforeConnect = fn })
}

// WithAfterConnect installs pgxpool.Config.AfterConnect - the usual place for
// custom pgx type registration. It runs on every new physical connection,
// including replacements, before the pool makes it available. The callback must
// honor ctx and be safe for concurrent calls on different connections. An error
// rejects that connection; a startup Ping failure prevents Fx startup.
// Repeated WithAfterConnect options replace the hook; nil clears it.
// Create database types before starting the pool.
func WithAfterConnect(fn func(context.Context, *pgx.Conn) error) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.AfterConnect = fn })
}

// WithPrepareConn installs pgxpool.Config.PrepareConn, called before a
// connection is handed out. For Acquire, true/nil permits use; true/error returns
// the connection to the pool and fails acquisition; false/error destroys it and
// fails acquisition; false/nil destroys it and retries within pgxpool's limit.
// AcquireAllIdle instead destroys any connection rejected by bool or error.
// The callback must honor ctx and support concurrent calls on different connections.
// Repeated options replace the hook; nil removes it.
func WithPrepareConn(fn func(context.Context, *pgx.Conn) (bool, error)) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.PrepareConn = fn })
}

// WithAfterRelease installs pgxpool.Config.AfterRelease. For a reusable
// connection, pgxpool runs it asynchronously before returning the connection to
// the idle pool. Return true to retain it or false to destroy it. Connections
// already closed, busy, expired or in a transaction bypass this callback.
// The callback has no context and must finish promptly; blocking it delays reuse
// and pool shutdown. Repeated options replace the hook; nil removes it.
func WithAfterRelease(fn func(*pgx.Conn) bool) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.AfterRelease = fn })
}

// WithBeforeClose installs pgxpool.Config.BeforeClose, called before pgxpool
// destroys an established pooled connection. It is not called for connections
// rejected during creation or handed to the caller through Hijack.
// The callback has no context and must finish promptly to avoid delaying cleanup.
// Repeated options replace the hook; nil removes it.
func WithBeforeClose(fn func(*pgx.Conn)) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.BeforeClose = fn })
}

// WithShouldPing installs pgxpool.Config.ShouldPing, deciding whether Acquire
// pings a connection before PrepareConn. Returning false skips that automatic
// check, not an explicit DB.Ping or the Fx startup/health Ping.
// The callback must honor ctx. Repeated options replace the hook; nil restores
// pgxpool's default idle-duration policy. This does not change HealthConfig.
func WithShouldPing(fn func(context.Context, pgxpool.ShouldPingParams) bool) Option {
	return withPoolConfig(func(cfg *pgxpool.Config) { cfg.ShouldPing = fn })
}

// WithTLS sets the connection's *tls.Config directly, overriding whatever the
// file-based Config.TLS and PGSSL environment settings before reading files. Use
// it when certificates are not files on disk - loaded from a secret manager,
// held in memory, or rotated at runtime: the application builds the *tls.Config
// and passes it here. It also removes libpq TLS/plaintext fallbacks so every
// connection attempt uses exactly this configuration. A nil value disables TLS.
func WithTLS(tlsConfig *tls.Config) Option {
	return func(o *options) {
		o.isTLSOverride = true
		o.tlsConfig = tlsConfig
	}
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
