# Changelog

All notable changes to this module are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.7.0] - 2026-09-03

### Added

- `QueryMetric.Kind` distinguishes queries, batches and COPY operations with
  bounded `query`, `batch` and `copy_from` labels via `QueryKind.String`.
- Batch metrics aggregate observed row counts and report one operation duration
  and error. Opt-in SQL joins queued statements; COPY leaves SQL empty.

### Fixed

- `WithQueryMetrics` now observes native `SendBatch` and `CopyFrom` through pgx
  hooks, including when composed with tracing. Duplicate batch end hooks report
  only once. Early COPY failures without an end hook remain unobservable.

## [0.6.0] - 2026-09-03

### Added

- `DB.FetchPage` and `Tx.FetchPage` return one page of struct rows together
  with the number of rows the filter matches in total.
- Package `page` holds the vocabulary of a paginated query - `Request`,
  `Query`, `Order`, `Cols` - and depends on nothing but the standard library,
  so a handler or a domain interface can speak about pages without importing a
  driver.
- `page.Make` and `page.Must` bind a plain `SELECT` to its sort policy:
  `Head` for a pinned leading order, the required `Tie` for the stabilizer,
  `Sortable` to narrow the whitelist, `SortKeyTag` to take the client's field
  names from another struct tag, and `CountSQL` to count over a cheaper
  statement.
- The count statement is reported to `QueryMetricFunc` under the caller's query
  name with a `.count` suffix.
- A logo in the README.

## [0.5.0] - 2026-09-01

### Changed

- `Module`, `ModuleFor`, and `Make` now return `*DB` instead of
  `*pgxpool.Pool`.
- `FetchRows`, `FetchRow`, `FetchValues`, and `FetchValue` are Go 1.27 generic
  methods on `DB` and `Tx`, replacing the package-level helpers.
- `DB` embeds `*pgxpool.Pool` and `Tx` embeds `pgx.Tx`; the native pgx API stays
  available without forwarding methods.

### Added

- `DB.Transaction` supplies a typed `*Tx`, committing on nil and rolling back on
  error or panic.
- `Tx.Transaction` and `Tx.BeginNested` provide typed pgx savepoints.

### Fixed

- No-row errors are returned unchanged from pgx, preserving `errors.Is` matches
  for both `pgx.ErrNoRows` and `sql.ErrNoRows`.

### Removed

- The exported `Querier` and `TxBeginner` interfaces.
- The package-level `Fetch*` and `Tx` functions.

## [0.4.0] - 2026-09-01

### Changed

- The package documentation carries the config fields, the options and the
  wiring, with the reasons behind them; the README is a landing page. The package
  comment moved from `pgfx.go` into `doc.go`.
- `Config.Validate` accumulates through `github.com/uchaloop/validate` instead of
  a hand-rolled slice and `errors.Join`. The messages and their order are
  unchanged.
- The module is built with Go 1.27, which the new dependency requires. A module
  that depends on this one has to declare 1.27 as well.

## [0.3.0] - 2026-08-25

### Changed

- `Host` and `Database` declare `notEmpty` instead of being checked in
  `Validate`. Whether a variable was supplied is the loader's question - only it
  tells an unset variable from an empty one, and only it knows the variable's
  name, since the prefix belongs to the application rather than to this module.
  `Validate` keeps what the values mean: that a host parses as an endpoint, that
  a pool size is not negative, that a minimum does not exceed its maximum.

### Removed

- The `koanf` struct tags and `config.example.toml`. Configuration is read from
  the environment only; `confx.Manifest[pgfx.Config]("postgres")` lists every
  variable a connection reads, which the example file used to do by hand.

## [0.2.1] - 2026-08-06

### Changed

- Reworked the README as concise, user-focused documentation.

## [0.2.0] - 2026-08-05

### Changed

- Updated `Config.Password` to the opaque
  `github.com/uchaloop/secret/v2.Secret`; construct values with `secret.New`.
- Every open `Config` field now supports an `env` override. Nested TLS, pool and
  timeout settings use `TLS_`, `POOL_` and `TIMEOUTS_` prefixes, enabling
  `confmaker/confx.ProvideNoFileDefault` without changing file-based loading.

## [0.1.1] - 2026-08-04

### Changed

- `Module` is now a package-level `fx.Option` variable, not a `Module()`
  function: wire it as `pgfx.Module`, not `pgfx.Module()`. `ModuleFor(name)` is
  unchanged. Breaking change for callers of the default module.

## [0.1.0] - 2026-08-04

### Added

- An Fx-first Postgres layer over pgx/pgxpool. A connection is a single
  `*pgxpool.Pool` built from a plain, serializable `Config`.
- `Config` (with nested `TLS`/`Pool`/`Timeouts`) carrying `koanf` (file) and
  `env` (environment) tags plus `Validate()`. `Host` reads from file or env (env
  overrides); `Password` is env-only (`koanf:"-"`), a masked
  `github.com/uchaloop/secret.Secret` kept out of every dump and log.
- `Make(ctx, cfg, opts...)`: build a pool from a Config, without Fx.
- `Module()` and `ModuleFor(name)`: Fx modules that consume a `pgfx.Config` from
  the container and provide a `*pgxpool.Pool` (pinged on start, closed on stop).
  `Module()` is the default, untagged connection; `ModuleFor(name)` is a named
  connection tagged `name:"<name>"` for a replica or shard. The application
  supplies the Config (typically via `confmaker/confx`), so pgfx reads no config
  source itself.
- Runtime `Option`s: `WithTLS` (in-memory `*tls.Config`), `WithTracing`
  (opt-in otelpgx spans), `WithTracer` (a custom tracer, e.g. X-Ray),
  `WithQueryMetrics`, `WithSQLInQueryMetrics`, `WithBeforeConnect`,
  `WithAfterConnect`, `WithRuntimeParam`, and other pgx pool escape hatches.
- Generic scan helpers `FetchRow`/`FetchRows`/`FetchValue`/`FetchValues` over a
  `Querier`, returning `sql.ErrNoRows` on no rows.
- `Tx(ctx, db, opts, fn)`: run a function inside a transaction; `db` is a
  `TxBeginner` interface (`*pgxpool.Pool` or `*pgx.Conn`), mirroring `Querier`.
- Opt-in OpenTelemetry query spans via [`exaring/otelpgx`](https://github.com/exaring/otelpgx)
  (`WithTracing`; under Fx, on when a `trace.TracerProvider` is supplied), plus a
  dependency-free per-query metrics callback `QueryMetricFunc` with `WithQueryName`.

- Decoupled from `confmaker`: the application supplies the `Config` (its `koanf`
  and `env` tags let `confmaker/confx` fill it), so pgfx pulls in no config or env
  stack - its only config-surface dependency is the zero-dep
  `github.com/uchaloop/secret` module for the masked password.

[Unreleased]: https://github.com/uchaloop/pgfx/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/uchaloop/pgfx/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/uchaloop/pgfx/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/uchaloop/pgfx/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/uchaloop/pgfx/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/uchaloop/pgfx/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/uchaloop/pgfx/releases/tag/v0.2.0
[0.1.1]: https://github.com/uchaloop/pgfx/releases/tag/v0.1.1
[0.1.0]: https://github.com/uchaloop/pgfx/releases/tag/v0.1.0
