# pgfx

[![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx)
[![License: MIT](https://img.shields.io/github/license/uchaloop/pgfx)](LICENSE)

A thin, Fx-first layer over [pgx](https://github.com/jackc/pgx) for Postgres: a
`*pgxpool.Pool` built from a plain config, generic query helpers, transactions,
tracing and lifecycle.

- **Config is data, runtime is options** - what survives a round trip through
  text is `Config`; a tracer, a metrics callback, an in-memory `*tls.Config` are
  `Option` values. Nothing is configured twice.
- **It reads no config source** - the application supplies the `Config`, so pgfx
  is tied to no particular loader.
- **A bad endpoint fails the start**, not the first query.
- **Helpers that work anywhere** - the same call inside a transaction and
  outside one.

```bash
go get github.com/uchaloop/pgfx
```

## Quick start

```go
fx.New(
	confx.Module(),
	confx.Provide[pgfx.Config]("postgres"),

	pgfx.Module,          // untagged *pgxpool.Pool, verified at start, closed at stop
)
```

A replica is another named connection - the name gives both the Fx tag and the
environment prefix:

```go
confx.ProvideNamed[pgfx.Config]("replica"),   // REPLICA_HOST, REPLICA_DATABASE, ...
pgfx.ModuleFor("replica"),
```

Without Fx: `pool, err := pgfx.Make(ctx, cfg)`.

## Queries

```go
order, err := pgfx.FetchRow[Order](
	ctx, pool,
	`SELECT id, amount FROM orders WHERE id = $1`, id,
)

err := pgfx.Tx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
	_, err := pgfx.FetchValue[int64](
		ctx, tx,
		`INSERT INTO orders (amount) VALUES ($1) RETURNING id`, amount,
	)

	return err
})
```

`FetchRows`, `FetchRow`, `FetchValues` and `FetchValue` take a pool, a
connection or a transaction. `Tx` commits on a nil error and rolls back on
anything else.

## What a deployment sets

```text
POSTGRES_HOST=localhost:5432
POSTGRES_DATABASE=orders
POSTGRES_USER=orders
POSTGRES_PASSWORD=...

POSTGRES_TLS_MODE=verify-full
POSTGRES_TLS_ROOT_CERT=/run/secrets/ca.crt

POSTGRES_POOL_MAX_CONNS=20
POSTGRES_TIMEOUTS_CONNECT=5s
```

Host and database are required; everything else keeps the pgx default until it
is set. `confx.Manifest[pgfx.Config]("postgres")` lists the whole set with types
and defaults.

## Documentation

The options, the config fields and the reasons behind them are in the package
documentation:
**[pkg.go.dev/github.com/uchaloop/pgfx](https://pkg.go.dev/github.com/uchaloop/pgfx)**.

## Acknowledgements

I am grateful to the authors of [pgx](https://github.com/jackc/pgx),
[Uber Fx](https://github.com/uber-go/fx), and
[otelpgx](https://github.com/exaring/otelpgx). Their work made this library
possible.

## License

[MIT](LICENSE)
