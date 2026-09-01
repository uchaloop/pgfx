/*
Package pgfx is a thin, Fx-first layer over pgx/pgxpool for Postgres. A pgfx
connection is a single *pgxpool.Pool built from a plain, serializable [Config],
with generic helpers for the two things every query does - read rows, read one
value - and a transaction that commits or rolls back on the error its callback
returns.

# Config and Option

The split between the two is the design. [Config] is data: it survives a round
trip through text, so a deployment fills it and a manifest lists it. Everything
that cannot survive that trip - a tracer, a metrics callback, a pool hook, an
in-memory *tls.Config - is an [Option] instead. Nothing is configured twice, and
nothing has to be expressed as a string that was never a string.

pgfx reads no configuration source of itself, which is what keeps it decoupled
from any particular loader. An application supplies the Config, typically
through github.com/uchaloop/confmaker, whose Manifest lists every variable a
connection reads:

	confx.Manifest[pgfx.Config]("postgres")

Host and Database have to be supplied. An empty User or Password falls through
to pgx's libpq-compatible defaults, so peer authentication and a .pgpass keep
working. Every pool and timeout field left at its zero value keeps the pgxpool
default, so a deployment sets what it means to change and nothing else.

# Wiring

[Module] provides an untagged *pgxpool.Pool - the single default connection. It
verifies the connection while the application starts, so a bad endpoint fails
the start rather than the first query, and closes the pool on shutdown.

[ModuleFor] adds a replica or a shard under a name, which gives both the Fx tag
and the environment prefix:

	fx.New(
		confx.Module(),
		confx.Provide[pgfx.Config]("postgres"),
		confx.ProvideNamed[pgfx.Config]("replica"),

		pgfx.Module,
		pgfx.ModuleFor("replica"),
	)

A named connection inherits nothing from the default one. Its database, user and
TLS settings are given again under its own prefix - a replica that silently
borrowed the primary's credentials would be the kind of thing noticed in
production.

Several databases are several named connections; there is no other mechanism.
Without Fx, [Make] builds a pool from a Config filled by hand.

# Queries and transactions

[FetchRows], [FetchRow], [FetchValues] and [FetchValue] take a pool, a
connection or a transaction - anything that can run a query - so a helper reads
the same inside a transaction as outside one:

	order, err := pgfx.FetchRow[Order](
		ctx, pool,
		`SELECT id, amount FROM orders WHERE id = $1`, id,
	)

[Tx] runs a callback in a transaction and decides by its error: nil commits,
anything else rolls back. The decision is not the callback's to make and not the
caller's to forget.

# TLS

TLS_MODE follows libpq, and verify-full with TLS_ROOT_CERT is the setting that
actually verifies the server. [WithTLS] takes an in-memory *tls.Config instead,
for an application that already assembled one - from a secret store, or from a
certificate that never touched the filesystem.

# Tracing and metrics

Under Fx, [Module] enables OpenTelemetry query spans by itself when a
trace.TracerProvider is in the graph. [WithTracing] configures otelpgx directly,
and [WithTracer] installs any pgx.QueryTracer.

Query duration is reported through a callback rather than to a metrics library,
so pgfx holds no opinion about where it goes:

	fx.Supply(pgfx.QueryMetricFunc(func(metric pgfx.QueryMetric) {
		// metric.Name, metric.Duration, metric.Err
	}))

[WithQueryName] attaches a stable name to a query through the context, because
the SQL itself is not a metric label - it varies, and it is unbounded.

	ctx = pgfx.WithQueryName(ctx, "orders.get_by_id")

The SQL is left out of the metric by default. [WithSQLInQueryMetrics] includes
it, for a system where a query text is not something to hand to a metrics
pipeline by accident.
*/
package pgfx
