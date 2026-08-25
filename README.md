# pgfx

[![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx)
[![License: MIT](https://img.shields.io/github/license/uchaloop/pgfx)](LICENSE)

PostgreSQL connections, queries, transactions, tracing, and Uber Fx lifecycle
management on top of [`pgx`](https://github.com/jackc/pgx).

## Installation

```bash
go get github.com/uchaloop/pgfx
```

## Configuration

A connection is configured from the environment, under the prefix the
application gives it:

```text
POSTGRES_HOST=localhost:5432
POSTGRES_DATABASE=orders
POSTGRES_USER=orders
POSTGRES_PASSWORD=...
POSTGRES_APP_NAME=orders-api

POSTGRES_TLS_MODE=verify-full
POSTGRES_TLS_ROOT_CERT=/run/secrets/ca.crt

POSTGRES_POOL_MAX_CONNS=20
POSTGRES_TIMEOUTS_CONNECT=5s
```

`POSTGRES_HOST` and `POSTGRES_DATABASE` must be supplied. When `USER` or
`PASSWORD` is empty, pgx uses its normal libpq-compatible defaults. Every pool
and timeout field left unset keeps the pgxpool default, so a deployment sets
only what it means to change.

`confx.Manifest[pgfx.Config]("postgres")` lists every variable a connection
reads, with its type and default.

For verified TLS, use `verify-full` with `TLS_ROOT_CERT`. Use `WithTLS` when the
application already has an in-memory `*tls.Config`:

```go
pgfx.WithTLS(tlsConfig)
```

## Fx

Load one default connection:

```go
fx.New(
	confx.Module(),
	confx.Provide[pgfx.Config]("postgres"),
	pgfx.Module,
)
```

`pgfx.Module` provides an untagged `*pgxpool.Pool`, verifies the connection
during startup, and closes the pool during shutdown.

Add a named replica or shard - the name gives both the Fx tag and the prefix,
so `replica` reads `REPLICA_HOST` and the rest:

```go
fx.New(
	confx.Module(),
	confx.Provide[pgfx.Config]("postgres"),
	confx.ProvideNamed[pgfx.Config]("replica"),
	pgfx.Module,
	pgfx.ModuleFor("replica"),
)
```

A replica inherits nothing from the default connection: its database, user and
TLS settings are given again under its own prefix.

Without Fx:

```go
pool, err := pgfx.Make(ctx, cfg)
```

## Queries

The generic helpers accept a pool, connection, or transaction:

```go
order, err := pgfx.FetchRow[Order](
	ctx,
	pool,
	`SELECT id, amount FROM orders WHERE id = $1`,
	id,
)

id, err := pgfx.FetchValue[int64](
	ctx,
	pool,
	`INSERT INTO orders (amount) VALUES ($1) RETURNING id`,
	amount,
)
```

Available helpers:

- `FetchRows[T]`
- `FetchRow[T]`
- `FetchValues[T]`
- `FetchValue[T]`

## Transactions

```go
err := pgfx.Tx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
	_, err := pgfx.FetchValue[int64](
		ctx,
		tx,
		`INSERT INTO orders (amount) VALUES ($1) RETURNING id`,
		amount,
	)

	return err
})
```

A nil callback error commits the transaction; a non-nil error rolls it back.

## Tracing

When a `trace.TracerProvider` is available in Fx, `pgfx.Module` enables
OpenTelemetry query spans automatically.

Without Fx:

```go
pool, err := pgfx.Make(
	ctx,
	cfg,
	pgfx.WithTracing(
		otelpgx.WithTracerProvider(provider),
		otelpgx.WithTrimSQLInSpanName(),
	),
)
```

A custom `pgx.QueryTracer` can be added with `WithTracer`.

## Query metrics

Record query duration through a callback:

```go
fx.Supply(pgfx.QueryMetricFunc(func(metric pgfx.QueryMetric) {
	// metric.Name, metric.Duration, metric.Err
}))
```

Attach a stable query name through the context:

```go
ctx = pgfx.WithQueryName(ctx, "orders.get_by_id")
```

Raw SQL is excluded by default. Enable it only when safe:

```go
pgfx.WithSQLInQueryMetrics(true)
```

## Runtime options

Additional options include:

- `WithBeforeConnect`
- `WithAfterConnect`
- `WithDefaultQueryExecMode`
- `WithStatementCacheCapacity`
- `WithRuntimeParam`

## Acknowledgements

I am grateful to the authors of [pgx](https://github.com/jackc/pgx),
[Uber Fx](https://github.com/uber-go/fx), and
[otelpgx](https://github.com/exaring/otelpgx). Their work made this library
possible.

## License

[MIT](LICENSE)
