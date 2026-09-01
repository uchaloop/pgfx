# pgfx

[![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx)
[![License: MIT](https://img.shields.io/github/license/uchaloop/pgfx)](LICENSE)

A thin, Fx-first layer over [pgx](https://github.com/jackc/pgx) for Postgres: a
connection built from a plain config, with generic query methods, tracing and
lifecycle.

- **Config is data, runtime is options** - what survives a round trip through
  text is `Config`; a tracer, a metrics callback, an in-memory `*tls.Config` are
  `Option` values. Nothing is configured twice.
- **It reads no config source** - the application supplies the `Config`, so pgfx
  is tied to no particular loader.
- **A bad endpoint fails the start**, not the first query.
- **No second query API** - `DB` and `Tx` add typed fetches while preserving pgx
  for execution, transactions and advanced operations.

```bash
go get github.com/uchaloop/pgfx
```

## Quick start

```go
fx.New(
	confx.Module(),
	confx.Provide[pgfx.Config]("postgres"),

	pgfx.Module,          // untagged *pgfx.DB, verified at start, closed at stop
)
```

A replica is another named connection - the name gives both the Fx tag and the
environment prefix:

```go
confx.ProvideNamed[pgfx.Config]("replica"),   // REPLICA_HOST, REPLICA_DATABASE, ...
pgfx.ModuleFor("replica"),
```

Without Fx: `db, err := pgfx.Make(ctx, cfg)`.

Fx provides only `*pgfx.DB`; the embedded pool is available as `db.Pool` and is
not registered separately.

## Queries

```go
order, err := db.FetchRow[Order](
	ctx,
	`SELECT id, amount FROM orders WHERE id = $1`, id,
)

total, err := db.FetchValue[int64](ctx, `SELECT count(*) FROM orders`)
```

```go
tag, err := db.Exec(ctx, `UPDATE orders SET status = $1 WHERE id = $2`, status, id)
```

`FetchRows` and `FetchRow` decode struct rows by the `db:"..."` tag;
`FetchValues` and `FetchValue` decode a single column into a scalar. The type to
decode into is an explicit type argument, because nothing in the arguments
implies it.

## Transactions

`Transaction` commits on nil and rolls back on an error or panic. Its `Tx` has
the same typed fetches as `DB` and embeds the native `pgx.Tx` API:

```go
err := db.Transaction(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) error {
	order, err := tx.FetchRow[Order](ctx, selectOrder, id)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, updateOrder, order.ID)
	return err
})
```

Use `tx`, not `db`, inside the callback: a call through `db` uses the pool and
does not participate in the transaction.

For manual ownership use the embedded native `db.Begin` or `db.BeginTx`; they
return `pgx.Tx`. `tx.BeginNested` and `tx.Transaction` create typed pgx
savepoints. The embedded pool also makes COPY, LISTEN, acquired connections and
the rest of the native API available directly on `DB`; the pool itself is
`db.Pool`.

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
