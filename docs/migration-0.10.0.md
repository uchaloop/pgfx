# Migrating from 0.9.1 to 0.10.0

Version 0.10.0 contains breaking API changes. See the
[release notes](../CHANGELOG.md#0100---2026-11-15) and
[current usage guide](../README.md).

There are no compatibility aliases for renamed APIs.

- When adopting health monitoring, pass
  `WithHealthSQLStates(DefaultHealthSQLStates()...)` explicitly to each Fx module
  that should react to the recommended SQLSTATE codes, or supply your own list.
  SQLSTATE errors require a match; transport/TLS failures without SQLSTATE and
  timeouts (including pool saturation) count independently. Unknown hook errors
  are ignored. Startup Ping remains unfiltered.
  Success or an ignored error resets the failure window.

- Replace the `pgfx.Module` option variable with `pgfx.MakeModule()`. To register
  types, pass `pgfx.WithAfterConnect(registerTypes)` to `MakeModule` or `MakeModuleFor`.
  Explicit options apply after injected tracing and metrics defaults.

| Previous name | Current name |
| --- | --- |
| `pgfx.Make` | `pgfx.MakeDB` |
| `page.Make` | `page.MakeQuery` |
| `pgfx.WithTracing` | `pgfx.WithTrace` |
| `page.Order.Desc` | `page.Order.IsDesc` |
| `page.Statements` | `page.Statement` |
| `page.Statements.PagingArgs` | `page.Statement.PageArgs` |
| `pgfx.Module` | `pgfx.MakeModule()` |
| `pgfx.ModuleFor` | `pgfx.MakeModuleFor` |
| `page.Asc` / `page.Desc` | `page.MakeAscOrder` / `page.MakeDescOrder` |
| `page.Must` | `page.MakeQueryOrPanic` |
| `page.Head` / `page.Tie` | `page.WithHead` / `page.WithTie` |
| `page.Sortable` | `page.WithSortable` |
| `page.SortKeyTag` | `page.WithSortKeyTag` |
| `Query.BuildRows` | `Query.BuildStatement` |

Response serialization remains the application's responsibility.

- Replace `Request.Number` / `Request.Size` with `Page` / `Limit`.
- `FetchPage` changes from `([]T, uint, error)` to `(page.Result[T], error)`.
  The result contains only `List []T` and `Total int64`; update response DTOs
  and page-count calculations that previously used a `uint` total.
  Replace `FetchPageRows` with `FetchPage` on both `DB` and `Tx`. Remove adapters
  for lookahead flags and cursor responses; pagination metadata can be derived by the
  application from the request and total when needed.
- `FetchAfter`, cursor request/result/statement types and cursor errors are
  removed. Migrate callers to numbered pages with `FetchPage`.
- Replace `Query.Build` with `BuildStatement`. `Statement` carries `Rows`, `Count`,
  `PageArgs` and `Limit`. The former `Statements.Offset` field is removed.
  `PageArgs` contains `Limit` and `Offset`, not the former `Limit+1` and `Offset`;
  there is no lookahead row. Append PageArgs only to the row query's filter
  arguments; execute Count with the original filter arguments alone.
- `BuildCount`, `CountSQL` and `FetchTotal` are removed. FetchPage performs the
  count automatically, including on empty pages. It does not create a transaction;
  use `Tx.FetchPage` under REPEATABLE READ for one snapshot across both queries.
- Inject `*pgfx.Connection` in Fx consumers and call `DB()` in their `OnStart`
  hook or after startup. Do not close the borrowed database. `MakeDB` still
  returns an owned database that the caller closes.
- Add explicit `db` tags for fields exposed through `WithSortKeyTag`, or use an
  explicit `WithSortable` mapping.

When enabling health checks, add `HealthModule` once at the application root.
Standalone `MakeDB` validates health settings but does not start a monitor.
See [health monitoring](../README.md#shutdown-on-sustained-database-unavailability) for setup and lifecycle details.

`pgfx/test` supplies a ready-to-use connection, without data isolation or migrations.
Tests own cleanup; use `PGFX_TEST_MAX_CONNS` when a scenario requires a pool limit.
See [Testing](../README.md#testing).
