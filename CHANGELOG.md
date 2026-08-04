# Changelog

All notable changes to this module are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.1.0]: https://github.com/uchaloop/pgfx/releases/tag/v0.1.0
