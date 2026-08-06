# pgfx

[![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx)
[![License: MIT](https://img.shields.io/badge/github/license/uchaloop/pgfx)](LICENSE)

PostgreSQL connections, queries, transactions, tracing, and Uber Fx lifecycle
management on top of [`pgx`](https://github.com/jackc/pgx).

## Installation

```bash
go get github.com/uchaloop/pgfx
```

## Configuration

```toml
[postgres]
host = "localhost:5432"
database = "orders"
user = "orders"
app_name = "orders-api"

[postgres.tls]
mode = "verify-full"
root_cert = "/run/secrets/ca.crt"

[postgres.pool]
max_conns = 20

[postgres.timeouts]
connect = "5s"
```

`host` and `database` are required. When `user` or `password` is empty, pgx uses
its normal libpq-compatible defaults. Passwords are environment-only:

```text
POSTGRES_PASSWORD
```

Other fields can also be overridden through variables such as
`POSTGRES_HOST`, `POSTGRES_POOL_MAX_CONNS`, and
`POSTGRES_TIMEOUTS_CONNECT`.

See [config.example.toml](config.example.toml) for every available field.

For verified TLS, use `verify-full` with `root_cert`. Use `WithTLS` when the
application already has an in-memory `*tls.Config`:

```go
pgfx.WithTLS(tlsConfig)
```

## Fx

Load one default connection:

```go
fx.New(
	confx.LoadDir("config"),
	confx.ProvideDefault[pgfx.Config]("postgres"),
	pgfx.Module,
)
```

`pgfx.Module` provides an untagged `*pgxpool.Pool`, verifies the connection
during startup, and closes the pool during shutdown.

Add a named replica or shard:

```toml
[postgres]
host = "primary:5432"
database = "orders"

[replica]
host = "replica:5432"
database = "orders"
```

```go
fx.New(
	confx.LoadDir("config"),
	confx.ProvideDefault[pgfx.Config]("postgres"),
	confx.Provide[pgfx.Config]("replica", "replica"),
	pgfx.Module,
	pgfx.ModuleFor("replica"),
)
```

Without a configuration file:

```go
fx.New(
	confx.ProvideNoFileDefault[pgfx.Config]("postgres"),
	pgfx.Module,
)
```

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
