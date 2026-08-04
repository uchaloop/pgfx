# pgfx

[![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A thin, **Fx-first** layer over [`pgx`](https://github.com/jackc/pgx)/`pgxpool`
for Postgres. A pgfx connection is a single `*pgxpool.Pool` built from a plain,
serializable `Config`. Runtime dependencies (tracer, query metrics, pool hooks,
an in-memory `*tls.Config`) are supplied through `Option`s, never through
`Config`.

`Module` consumes a `pgfx.Config` from the
[Uber Fx](https://github.com/uber-go/fx) container and hands back a ready pool;
`ModuleFor(name)` adds a replica or another shard. **Multiple databases are just
multiple named connections.** pgfx does not read any config source itself, so it
stays decoupled: the application supplies each `Config`, typically loaded from a
file and the environment with
[`confmaker/confx`](https://github.com/uchaloop/confmaker). pgfx depends only on
[`github.com/uchaloop/secret/v2`](https://pkg.go.dev/github.com/uchaloop/secret/v2)
(for the masked password), not on any config or env stack.

## Install

```bash
go get github.com/uchaloop/pgfx
```

## Config

`Config` is a plain, serializable struct. Every open field can be read from a
file or the environment (env wins). `Password` is env-only - a
[`secret.Secret`](https://pkg.go.dev/github.com/uchaloop/secret/v2) that masks
itself in logs and dumps, and placing it in the file is rejected.

**Host** and **Database** are required. **User** and **Password** are optional:
when empty they fall back to libpq's defaults (`PGUSER` / the OS user, and
`PGPASSWORD` / `.pgpass`), so peer auth and passwordless connections work.

The section name is the application's choice - it is passed to `confx` when
wiring (below). A typical `[postgres]` table:

```toml
# config/local.toml
[postgres]
host     = "localhost:5432"   # "host" or "host:port"; port omitted -> 5432
database = "orders"
user     = "orders"

  [postgres.tls]
  mode      = "verify-full"   # verify-full + root_cert for MITM protection
  root_cert = "/run/secrets/ca.crt"

  [postgres.pool]
  max_conns = 20
```

> SECURITY: `sslmode=prefer`/`allow` fall back to unencrypted and skip
> certificate verification; use `verify-full` with a `root_cert` for MITM
> protection.

TLS certificates in the file are **paths** (`sslcert`/`sslkey`/`sslrootcert`),
read by libpq. When certificates are not files - loaded from a secret manager or
held in memory - pass a built `*tls.Config` with `pgfx.WithTLS`, which overrides
the file settings and disables libpq TLS/plaintext fallbacks, ensuring every
connection attempt uses exactly that configuration. Passing `nil` explicitly
disables TLS.

See [`config.example.toml`](config.example.toml) for every field with comments.

## Fx wiring

pgfx consumes a `pgfx.Config` from the container; the application provides it.
The recommended source is
[`confmaker/confx`](https://github.com/uchaloop/confmaker), which loads a TOML
section plus environment into the `Config`. For a single connection: load the
file, provide the default `Config`, and add `pgfx.Module`:

```go
fx.New(
	confx.LoadModule("config/local.toml"),
	confx.ProvideDefault[pgfx.Config]("postgres"), // untagged Config, POSTGRES_* env
	pgfx.Module,                                   // untagged *pgxpool.Pool
)
```

Without a configuration file, fill the same `Config` entirely from environment
variables:

```go
fx.New(
	confx.ProvideNoFileDefault[pgfx.Config]("postgres"),
	pgfx.Module,
)
```

The main variables are `POSTGRES_HOST`, `POSTGRES_DATABASE`, `POSTGRES_USER`,
`POSTGRES_PASSWORD` and `POSTGRES_APP_NAME`. Nested settings use structural
prefixes, for example `POSTGRES_TLS_MODE`, `POSTGRES_POOL_MAX_CONNS` and
`POSTGRES_TIMEOUTS_CONNECT`.

`pgfx.Module` provides an **untagged** `*pgxpool.Pool`, so repositories just
depend on `*pgxpool.Pool` - no tags, no wrappers:

```go
type OrderRepo struct {
	db *pgxpool.Pool
}

func MakeOrderRepo(db *pgxpool.Pool) *OrderRepo {
	return &OrderRepo{db: db}
}
```

Add a named connection for a replica or another shard: provide a tagged `Config`
and add `pgfx.ModuleFor(name)`, which provides a `*pgxpool.Pool` tagged
`name:"<name>"` (a consumer selects it with `fx.ParamTags`). Each named
connection is its own top-level section:

```toml
[postgres]                      # default connection, POSTGRES_* env
host = "primary:5432"
database = "orders"

[replica]                       # env REPLICA_*
host = "replica:5432"
database = "orders"
```

```go
fx.New(
	confx.LoadModule("config/local.toml"),
	confx.ProvideDefault[pgfx.Config]("postgres"),         // untagged primary
	confx.Provide[pgfx.Config]("replica", "replica"),      // tagged name:"replica"
	pgfx.Module,
	pgfx.ModuleFor("replica"),
)
```

Both are pinged on start (fail-fast on a dead database) and closed on stop, and
both take an optional `trace.TracerProvider` and `QueryMetricFunc` from the
container.

Without Fx, build a pool directly with `pgfx.Make(ctx, cfg, opts...)` from a
`Config` you fill yourself.

## Querying

Generic scan helpers work on any `Querier` (`*pgxpool.Pool`, `*pgx.Conn`,
`pgx.Tx`):

```go
func (r *OrderRepo) Get(ctx context.Context, id int64) (Order, error) {
	const sql = `SELECT id, amount FROM orders WHERE id = $1`
	
	return pgfx.FetchRow[Order](ctx, r.db, sql, id)
}

func (r *OrderRepo) Create(ctx context.Context, amount int64) (int64, error) {
	const sql = `INSERT INTO orders (amount) VALUES ($1) RETURNING id` 
	
	return pgfx.FetchValue[int64](ctx, r.db, sql, amount)
}
```

```go
order, err := repo.Get(ctx, id)
if errors.Is(err, sql.ErrNoRows) {
	// not found
}

id, err := repo.Create(ctx, amount)
```

Transactions - `Tx` takes a `TxBeginner` (`*pgxpool.Pool` or `*pgx.Conn`),
mirroring `Querier`:

```go
err := pgfx.Tx(
	ctx,
	repo.db,
	pgx.TxOptions{},
	func(tx pgx.Tx) error {
            _, err := pgfx.FetchValue[int64](ctx, tx, `INSERT ... RETURNING id`, ...)
            return err // non-nil -> rollback; nil -> commit 
	},
	)
```

## Tracing and query metrics

**Tracing is opt-in.** OpenTelemetry query spans come from
[`exaring/otelpgx`](https://github.com/exaring/otelpgx). Under Fx, tracing turns
on when a `trace.TracerProvider` is supplied to the container; otherwise there is
no tracer. Without Fx, enable it with `pgfx.WithTracing(...)`, configured by
otelpgx's own options:

```go
// Fx: supplying a TracerProvider enables otelpgx spans.
fx.Supply(fx.Annotate(tp, fx.As(new(trace.TracerProvider))))

// non-Fx:
pool, _ := pgfx.Make(ctx, cfg,
	pgfx.WithTracing(otelpgx.WithTracerProvider(tp), otelpgx.WithTrimSQLInSpanName()))
```

A different backend (for example AWS X-Ray) plugs in through
`pgfx.WithTracer(customTracer)`, which composes alongside otelpgx.

**Query metrics** are a separate, dependency-free callback (otelpgx does spans,
not per-query metrics):

```go
fx.Supply(pgfx.QueryMetricFunc(func(m pgfx.QueryMetric) { /* record m.Name, m.Duration */ }))
```

`QueryMetric.SQL` is empty unless `WithSQLInQueryMetrics(true)`; label metrics by
`QueryMetric.Name` (`WithQueryName(ctx, "orders.get_by_id")`), never by SQL.

## Acknowledgements

pgfx stands on [jackc/pgx](https://github.com/jackc/pgx),
[uber-go/fx](https://github.com/uber-go/fx) and
[exaring/otelpgx](https://github.com/exaring/otelpgx). Thanks to their authors
and maintainers.

## License

[MIT](LICENSE).
