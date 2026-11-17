# pgfx

<p align="center">
  <img src="logo.svg" alt="pgfx - Go gopher with elephant ears and a short trunk beside the pgfx wordmark" width="480">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx)
[![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/github/license/uchaloop/pgfx)](LICENSE)

**pgfx extends [pgx](https://github.com/jackc/pgx) with typed fetches, pagination,
transaction callbacks and optional Fx integration.** SQL stays explicit; the
embedded pgx pool and transaction expose the native API. The application owns
configuration loading, access control and query design. pgfx is not an ORM.

[Installation](#installation) · [Quick start](#quick-start) · [Lifecycle](#how-it-works) · [Pagination](#pagination) · [Testing](#testing) · [Reference](#reference)

## Installation

Requires Go 1.27 or later, as declared in [go.mod](go.mod).

```sh
go get github.com/uchaloop/pgfx
```

This README describes the current source tree, including changes in
[0.12.0](CHANGELOG.md#0120---2026-11-17). The install command resolves the latest
published version, which may have a different API. For these examples, use this
checkout; for a published version, read the README at its tag.
The API may change between 0.x releases without backward compatibility.

## Quick start

From the repository root, use a disposable PostgreSQL database. For example,
with Docker installed:

```sh
docker run --name pgfx-docs -d \
  -p 127.0.0.1:55432:5432 \
  -e POSTGRES_DB=pgfx_test -e POSTGRES_PASSWORD=postgres postgres:17-alpine
until docker exec pgfx-docs pg_isready -h 127.0.0.1 -U postgres -d pgfx_test; do sleep 1; done

export PGFX_TEST_HOST=localhost:55432
export PGFX_TEST_DATABASE=pgfx_test
export PGFX_TEST_USER=postgres
export PGFX_TEST_PASSWORD=postgres
```

The following complete program is [examples/quickstart/main.go](examples/quickstart/main.go).
It creates a pool, verifies connectivity and runs a query without requiring a schema.
The `PGFX_TEST_*` variables are read by this example, not automatically by pgfx.
TLS is disabled only for this local database; use verified TLS for remote servers.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/secret/v2"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := pgfx.Config{
		Host:     os.Getenv("PGFX_TEST_HOST"),
		Database: os.Getenv("PGFX_TEST_DATABASE"),
		User:     os.Getenv("PGFX_TEST_USER"),
		Password: secret.New(os.Getenv("PGFX_TEST_PASSWORD")),
		TLS:      pgfx.TLSConfig{Mode: "disable"}, // Local disposable database only.
	}
	db, err := pgfx.MakeDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// MakeDB creates the pool; Ping verifies connectivity.
	if err := db.Ping(ctx); err != nil {
		return err
	}

	value, err := db.FetchValue[int](ctx, "SELECT 1")
	if err != nil {
		return err
	}

	fmt.Println(value)

	return nil
}
```

Run it from this checkout:

```sh
go run ./examples/quickstart
```

Expected standard output: `1`. When finished with the local database:

```sh
docker stop pgfx-docs
docker rm pgfx-docs
```

## How it works

1. Supply a `Config` with connection data and optional runtime `Option` values.
2. `MakeDB` validates configuration and creates a pool. It does not verify
   connectivity; call `Ping` when startup verification is required.
3. Execute SQL through typed helpers or the embedded pgx API. Close the pool
   after all consumers finish.

`DB` embeds `*pgxpool.Pool`, and `Tx` embeds `pgx.Tx`. COPY, batches, acquired
connections and other native operations remain available.

### Fx lifecycle and named connections

`MakeModule()` provides `*DB` directly. Fx constructs providers lazily, so
register a consumer of `*DB`. The module creates and pings its embedded pool in
`OnStart` and closes it in `OnStop`. Constructors only store the pointer; queries
run in consumer `OnStart` hooks or after successful startup. Consumers finish
work in their `OnStop` hooks, before the pool is closed. Do not close the
lifecycle-owned database yourself.

The complete [Fx example](examples/fx/main.go) registers a consumer, queries
`SELECT 1` during startup and stops the application. With the same environment
and database as Quick start, run `go run ./examples/fx`. It prints `1` to stdout
and Fx lifecycle diagnostics to stderr.

For a replica or shard, supply a `Config` annotated with
``fx.ResultTags(`name:"replica"`)``, register `MakeModuleFor("replica")`, and select
the handle with the matching `fx.ParamTags`. See the compiled
[`ExampleMakeModuleFor`](example_test.go). The native pool is not separately
registered in Fx.

### Registering PostgreSQL types through Fx

`MakeModule(opts ...Option)` and `MakeModuleFor(name, opts ...Option)` accept the same
runtime options as `MakeDB`. Pass `WithAfterConnect` to register types on every
new physical connection, including connections created after recovery:

```go
func registerTypes(ctx context.Context, conn *pgx.Conn) error {
    for _, name := range []string{"public.order_status", "public.order_status[]"} {
        typ, err := conn.LoadType(ctx, name)
        if err != nil {
            return err
        }
        conn.TypeMap().RegisterType(typ)
    }
    return nil
}
```

Create `public.order_status` in a migration before starting the application.
The enum is registered before its array. This fragment requires `context` and
`github.com/jackc/pgx/v5`. Wire it alongside your Config and DB consumer:

```go
pgfx.MakeModule(pgfx.WithAfterConnect(registerTypes))
// For a named Config and consumer instead:
pgfx.MakeModuleFor("replica", pgfx.WithAfterConnect(registerTypes))
```

Each module's options affect only its own pool. Explicit options apply after
injected tracing and metrics defaults. Later hook options replace earlier ones;
`WithAfterConnect(nil)` clears the hook, and nil options are ignored. Combine
multiple registration steps in one callback. Callbacks must honor their context
and support concurrent calls on different connections. A callback error rejects
the connection. If startup Ping fails, Fx startup fails and closes the temporary
pool; the already-provided DB keeps a nil Pool.
See the compiled [type-registration example](example_test.go).

### Pool lifecycle hooks

The following options work with `MakeDB`, `MakeModule` and `MakeModuleFor`. They expose
native pgxpool callbacks without changing their semantics:

| Option | Callback | Purpose |
| --- | --- | --- |
| `WithPrepareConn` | `func(context.Context, *pgx.Conn) (bool, error)` | Prepare or reject a connection before acquisition |
| `WithAfterRelease` | `func(*pgx.Conn) bool` | Retain (`true`) or discard (`false`) a reusable released connection |
| `WithBeforeClose` | `func(*pgx.Conn)` | Observe a pooled connection before destruction |
| `WithShouldPing` | `func(context.Context, pgxpool.ShouldPingParams) bool` | Decide whether Acquire performs an automatic ping |

For example, prepare session state whenever a connection is acquired. This
fragment assumes an existing Config and DB consumer and imports
`context`, `github.com/jackc/pgx/v5` and `github.com/uchaloop/pgfx`:

```go
pgfx.MakeModule(
    pgfx.WithPrepareConn(func(ctx context.Context, conn *pgx.Conn) (bool, error) {
        _, err := conn.Exec(ctx, "SELECT set_config('statement_timeout', '5s', false)")
        if err != nil {
            return false, err
        }

        return true, nil
    }),
)
```

For Acquire, `PrepareConn` returning `true, nil` permits use; `true, err` fails
acquisition but retains the connection; `false, err` fails and destroys it;
`false, nil` destroys it and retries within pgxpool's limit. `AcquireAllIdle`
instead destroys any rejected connection. This hook also affects pool acquisition
by Fx startup and health checks; do not require HTTP-request-only context values.

Callbacks can run concurrently on different connections. The connection is
borrowed only for the callback: do not retain it or re-enter the same pool.
Honor supplied contexts and bound any I/O. `AfterRelease` runs asynchronously
and is skipped for connections already unsuitable for reuse. `BeforeClose` does
not cover connections rejected during creation or transferred via `Hijack`.
These two hooks have no context; keep them prompt so reuse and shutdown finish.

Later options replace earlier hooks; nil removes the callback. For `ShouldPing`,
nil restores pgxpool's idle-duration policy. Returning false skips only the
automatic acquisition ping, not explicit `DB.Ping` or Fx startup/health checks.
See the option GoDoc for details and the compiled pool-hook example in
[example_test.go](example_test.go).

### Shutdown on sustained database unavailability

Fx connections can opt into background pool checks. The zero-value policy is
disabled. Add this assignment after constructing `cfg` (imports: `pgfx` and
`time`):

```go
cfg.Health = pgfx.HealthConfig{
    IsEnabled:    true,
    Interval:     10 * time.Second,
    Timeout:      3 * time.Second,
    FailureAfter: time.Minute,
}
```

Add `HealthModule` **once at the application root**, alongside `MakeModule` and a
consumer of `*DB`. This fragment assumes `cfg` above and an existing
`registerService` consumer (additional import: `go.uber.org/fx`):

```go
fx.New(
    pgfx.HealthModule,
    fx.Supply(cfg),
    pgfx.MakeModule(pgfx.WithHealthSQLStates(pgfx.DefaultHealthSQLStates()...)),
    fx.Invoke(registerService),
).Run()
```

`HealthModule` makes concurrent injected shutdown requests safe. Keep it at the
root: do not nest it inside `fx.Module`, replace the decorated shutdowner or
retain its underlying instance in another decorator. Enabled checks without the
adapter fail construction with `ErrHealthModuleRequired`; disabled checks need
no adapter. See the [HealthModule contract](health_shutdown.go) for details.

The complete
[health monitor example](examples/health/main.go) uses `fx.App.Run`, prints `1`
on startup, and remains running until a signal or health-triggered shutdown.
With the Quick start database and environment, run `go run ./examples/health`.
For the service's actual exit status, build and run the executable directly;
`go run` reports a child's exit status through the Go command.

After the connection's startup ping succeeds, significant failed checks can request
shutdown with exit code 1. Success or an error outside the policy resets the
failure window; an ignored error is not logged as recovery. Detection is sampled, not an exact one-minute
deadline. The [HealthConfig contract](health.go) defines the intervals, per-check
timeout (including pool acquisition) and failure-window timing.

A saturated pool can fail the check even while PostgreSQL is reachable. This
policy detects inability to ping through the pool; it does not diagnose a broken
network connection or guarantee every query can execute. Set thresholds for your
workload. Startup connectivity failures still fail startup immediately.

Choose SQLSTATE codes explicitly with `WithHealthSQLStates`. The option replaces
that connection's list; the last option wins, and an empty list or no option
means SQLSTATE errors do not trigger shutdown. Arguments are copied immediately
and deduplicated. Invalid codes (anything other than five uppercase ASCII letters
or digits) fail construction even when monitoring is disabled.
`DefaultHealthSQLStates()` returns a fresh list of recommended connection,
authentication and server-unavailability codes; see [the exact list](health_errors.go).

Transport/TLS failures without SQLSTATE, closed connections and check timeouts
(including waiting for a saturated pool) count independently of that list.
A SQLSTATE inside a connection error is still subject to the selected list.
Unknown hook errors are ignored. These rules apply only to background checks;
any startup Ping error still fails startup, and ordinary query errors are not monitored.

For different policies, pass options to each named module. This fragment assumes
named Config providers and consumers for both connections, plus HealthModule at
the application root:

```go
pgfx.MakeModuleFor("primary", pgfx.WithHealthSQLStates(pgfx.DefaultHealthSQLStates()...))
pgfx.MakeModuleFor("analytics", pgfx.WithHealthSQLStates("53300", "57P03"))
```

Each monitor has its own failure window. Recovery of one connection cannot reset
another's window; sustained significant failures of any enabled connection can
stop the entire application. The option itself does not enable monitoring.

`MakeModuleFor` uses the policy of its named config. Enable it only for connections
whose unavailability should stop the entire application. `MakeDB` validates
`Health` but never starts a monitor or requests shutdown. `Pool.HealthPeriod`
is pgxpool's separate idle-connection maintenance setting.

All three durations are required and positive when enabled, with
`Timeout <= Interval <= FailureAfter`; no durations are defaulted. A loader using
the `postgres` prefix can set `POSTGRES_HEALTH_IS_ENABLED`,
`POSTGRES_HEALTH_INTERVAL`, `POSTGRES_HEALTH_TIMEOUT` and
`POSTGRES_HEALTH_FAILURE_AFTER`.

The monitor logs failure, recovery and shutdown transitions through an optional
Fx-provided `*slog.Logger`, falling back to `slog.Default()`. It includes the
connection name and, for significant failures, the category and SQLSTATE, but
never the raw ping error or credentials. Logging callbacks
must not block. The connection's stop hook cancels and joins the monitor before
closing its pool; consumer stop hooks run first in Fx's reverse lifecycle order.

`fx.App.Run` handles the signal and exit code. Applications using `Start`/`Stop`
manually must receive `app.Wait()`, call `Stop`, and propagate `ExitCode` from
`main`. Kubernetes owns any restart policy. pgfx does not call `os.Exit` or
restart processes. Fx chooses which shutdown signal is consumed first;
concurrent requests do not give the health failure priority over another signal.
Shutdown hooks must release borrowed connections so pool closure can finish.

## Configuration and observability

`Config` is data: host, database, credentials, TLS, pool sizes and connection
timeouts. Host and database are required. Unset optional fields retain their
documented defaults; empty credentials can use libpq environment or password
file fallbacks. See the current [Config documentation](config.go)
and [package overview](doc.go).

Runtime dependencies use options such as `WithTLS`, `WithTrace`, `WithTracer`
and `WithQueryMetrics`. For remote TLS, use `verify-full` with a trusted root
certificate. The default `prefer` mode may fall back to unencrypted transport.

Use `WithQueryName` for stable metric names. `WithQueryMetrics` observes queries,
batches and COPY; callbacks must be concurrency-safe and cheap. SQL text is
excluded by default. `WithSQLInQueryMetrics` opts in to SQL text, which may
contain sensitive literals. See the [metrics example](example_test.go).

### Optional configuration loaders

[confmaker](https://github.com/uchaloop/confmaker) and
[confx](https://github.com/uchaloop/confx) can load `Config` using its `env` tags.
They are optional and are not dependencies of pgfx. `Config.EnvPrefix()` declares
`POSTGRES_`; pgfx does not read environment variables itself.

With the updated confmaker built-in engine and confx default-registration API,
`confx.Provide[pgfx.Config]()` supplies an untagged config from `POSTGRES_*`.
An explicit `postgres` name uses the same prefix without duplication.
`confx.ProvideNamed[pgfx.Config]("master")` retains the Fx name `master` and reads
`MASTER_POSTGRES_*`; `replica` similarly reads `REPLICA_POSTGRES_*`.
Nested settings retain their suffixes, such as `MASTER_POSTGRES_TLS_MODE`.

This changes ENV names for existing named configurations when adopting a
loader that supports type prefixes. Migrate the binary and deployment ENV keys
together. To retain the old names explicitly, use
`confx.ProvideNamed[pgfx.Config]("master", confmaker.WithPrefix("MASTER_"))`.
The override replaces the complete ENV prefix without changing the Fx name.
Older loaders keep their explicit-name behavior and do not infer this prefix.

For such a loader, representative settings are:

```text
POSTGRES_HOST=db.example.com:5432
POSTGRES_DATABASE=orders
POSTGRES_USER=orders
POSTGRES_TLS_MODE=verify-full
POSTGRES_TLS_ROOT_CERT=/run/secrets/ca.crt
POSTGRES_POOL_MAX_CONNS=20
POSTGRES_TIMEOUTS_CONNECT=5s
```

Supply `POSTGRES_PASSWORD` through your secret management system. Consult the
loader's versioned documentation for registration and manifest APIs.

## Queries and result buffers

| Method | Result |
| --- | --- |
| `FetchRow[T]` | Exactly one struct row |
| `FetchOptionalRow[T]` | Zero or one struct row, with a found flag |
| `FetchRows[T]` | Zero or more struct rows |
| `FetchValue[T]` | Exactly one scalar value |
| `FetchValues[T]` | Zero or more scalar values |
| `FetchRowsInto` / `FetchValuesInto` | Append rows to a caller-owned slice |

All methods exist on both `DB` and `Tx`. Struct fields map through `db` tags,
falling back to field names; JSON/JSONB fields use pgx codecs. SQL values belong
in placeholders such as `$1`, with arguments passed separately. See the
[compiled query examples](example_test.go).

The `Into` methods preserve existing elements. Pass `items[:0]` to reuse storage
only after its previous consumers finish. Always use the returned slice because
append may allocate. Capacity does not limit row count. An error returns nil,
but the supplied backing array may already contain writes. See the
[buffer reuse examples](query_example_test.go) for both struct and scalar slices.

### Optional single rows

`DB.FetchOptionalRow[T]` and `Tx.FetchOptionalRow[T]` use the same struct mapping
as `FetchRow`, but return `(value, isFound, err)`:

See the [complete optional-row example](query_example_test.go) for both found
and absent results, including error handling.

No rows returns a zero value, `false`, `nil`. One row returns the decoded value,
`true`, `nil`. Multiple rows return `pgx.ErrTooManyRows`; query, decoding and
iteration errors remain errors, with a zero value and `false`. `FetchRow` still
requires exactly one row and returns `pgx.ErrNoRows` for an empty result.

## Pagination

| API | Use when |
| --- | --- |
| `page.MakePager` and `Pager.Fetch` | Numbered pages need normalized limits and one database snapshot. |
| `page.Fetch` | The caller supplies pagination and manages the callbacks' resources. |

Both return `page.Result[T]` with `List []T` and `Total int64`.
Response serialization belongs to the application.

### Numbered pages with a consistent snapshot

`Pager.Fetch` calculates `LIMIT/OFFSET` from `page.Params{Page, Limit}` and
runs list, then count, in one read-only `REPEATABLE READ` transaction. Both
callbacks must use the supplied `*pgfx.Tx` and context. Rows retain `db`-tag
mapping; JSON tags are unrelated to database decoding.

`page.MakePager(page.Config{})` uses minimum/default/maximum limits of 1/10/100.
Each zero configuration field uses its default. The constructor rejects negative
limits and requires minimum <= default <= maximum. A page below 1 becomes 1;
a nonpositive requested limit uses the default; a positive limit is clamped to
the configured range. `pager.Bounds(params)` performs the same calculation
without accessing the database and rejects offset overflow with `page.ErrParams`.

The following fragment assumes `ctx`, an initialized `db`, an `orders` table,
and an `Order` type with an `ID` field tagged `db:"id"`. Import `context`,
`github.com/uchaloop/pgfx` and `github.com/uchaloop/pgfx/page`; the enclosing
function returns `(page.Result[Order], error)`. Construct the pager once and
reuse it across requests.

```go
pager, err := page.MakePager(page.Config{MinLimit: 1, DefaultLimit: 20, MaxLimit: 100})
if err != nil {
    return page.Result[Order]{}, err
}

return pager.Fetch(
    ctx,
    db,
    page.Params{Page: 2, Limit: 20},
    func(ctx context.Context, tx *pgfx.Tx, bounds page.Bounds) ([]Order, error) {
        return tx.FetchRows[Order](
            ctx,
            "SELECT id FROM orders ORDER BY id LIMIT $1 OFFSET $2",
            bounds.Limit,
            bounds.Offset,
        )
    },
    func(ctx context.Context, tx *pgfx.Tx) (int64, error) {
        return tx.FetchValue[int64](ctx, "SELECT COUNT(*) FROM orders")
    },
)
```

Count runs
even for an empty page; a successful empty list is non-nil. Errors from callbacks,
context cancellation, begin or commit return a zero result. A list error skips
count. The helper owns the transaction, but does not close the borrowed DB.
Callbacks must not commit or roll back the transaction themselves.

### Independent callbacks

`page.Fetch` does not calculate bounds or open a transaction. It calls list,
then count, including for empty lists. As with `Pager.Fetch`, callback errors
return a zero result and a successful nil list becomes non-nil.

`page.WithParallel()` runs callbacks concurrently using their supplied contexts.
The first recorded error cancels the sibling; Fetch waits for both to finish.
Callbacks must cooperate with cancellation. Never share a transaction or
connection between parallel callbacks. Pool-backed callbacks use separate
connections and do not guarantee a common snapshot or faster execution.
The context and callbacks must be non-nil.

See the [complete examples](page/example_test.go) for sequential, parallel and
caller-owned transaction variants, plus the main `Pager.Fetch` scenario.
They use `pgfx/test` and `generate_series`, without application tables; see
[PostgreSQL integration tests](CONTRIBUTING.md#postgresql-integration-tests)
for setup and execution.

For either API, keep list/count filters identical and exclude LIMIT/OFFSET from
the count. Preserve DISTINCT, grouping and join semantics. SQL and filter values
belong to the application: parameterize values and whitelist client-controlled
sort columns and directions. Never interpolate raw client sort text.

### Sorting and indexes

Use a deterministic order with a unique tie-breaker, including for results
multiplied by joins. For `ORDER BY score DESC, id ASC`, a matching index is
`(score DESC, id ASC)`. Default NULL placement follows PostgreSQL; custom NULL
placement may require another index or a sort. Expose only sorts that your
workload can support. Indexes do not make arbitrary joins, filters or expressions
cheap.

LIMIT/OFFSET supports direct page navigation, but deep offsets make PostgreSQL
walk skipped rows, and an exact count processes the complete base result. The
returned-row limit does not bound database work. See
[pagination-lab](CONTRIBUTING.md#pagination-lab) for sequential and parallel row
and count plan checks, [pagination examples](page/example_test.go).

## Transactions

`DB.TransactionValue` returns a typed value after a successful commit. This
fragment assumes `ctx`, an initialized `db`, `amount`, an `orders` table and
imports for `pgx` and `pgfx`; its enclosing function returns `(int64, error)`:

```go
id, err := db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int64, error) {
    return tx.FetchValue[int64](ctx, "INSERT INTO orders (amount) VALUES ($1) RETURNING id", amount)
})
if err != nil {
    return 0, err
}
return id, nil
```

Any error returns the zero value and the transaction error unchanged. Rollback
error precedence and panic cleanup follow pgx, just as with `Transaction`.
`Tx.TransactionValue(ctx, fn)` uses a savepoint; its success does not commit the
outer transaction. Return fully consumed values, not rows or resources tied to
the transaction. These methods do not retry operations.

`DB.Transaction` commits when its callback returns nil and rolls back on error
or panic. Use the callback's `*Tx` for all operations in that transaction; calls
through `DB` use the pool independently. `Tx.Transaction` and `Tx.BeginNested`
provide typed pgx savepoints. Native `DB.Begin` and `DB.BeginTx` return `pgx.Tx`.
See the [transaction examples](example_test.go) and the
[complete TransactionValue example](transaction_value_example_test.go).

See [Pagination](#pagination) for transaction ownership in pagination helpers.

## Errors and diagnostics

- `FetchRow` and `FetchValue` return `pgx.ErrNoRows` or `pgx.ErrTooManyRows` unless exactly
  one row is returned. Plural fetches accept empty results. Use `errors.Is` for
  sentinel errors and `errors.As` for PostgreSQL errors.
- For Fx startup and shutdown requirements, see
  [Fx lifecycle and named connections](#fx-lifecycle-and-named-connections).
- Use `errors.Is(err, page.ErrParams)` for invalid pager configuration or offset
  overflow. Validate application filters and allowed sort fields separately;
  neither pagination API builds SQL.
- Pass a context with an appropriate deadline and handle the returned error.
  Avoid logging credentials, raw filter arguments or SQL containing secrets.

### Classifying PostgreSQL errors

`AsPgError` extracts the first `*pgconn.PgError` through `errors.AsType`, including
wrapped and joined errors. `SQLState` returns its code; `IsSQLState` compares a
nonempty code. Neither replaces the original error. Common predicates include
`IsUniqueViolation`, `IsForeignKeyViolation`, `IsNotNullViolation`,
`IsCheckViolation`, `IsExclusionViolation`, `IsSerializationFailure` and
`IsDeadlockDetected`; see [errors.go](errors.go) for the complete list.

Use `IsConstraint(err, "23505", "users_email_key")` to match SQLSTATE and
constraint name on the same first PostgreSQL error. See the
[error classification examples](helpers_example_test.go). Both arguments must be
nonempty and match exactly. Constraint names are not globally unique; use this
when the query's table/schema is known, or inspect `AsPgError` for that context. These helpers classify errors; they do not decide whether retrying
an operation is safe. A network failure can leave its outcome unknown.
Serialization failures and deadlocks require consideration of the entire
transaction and its external effects. PostgreSQL error details may contain
sensitive values; classification does not make raw errors safe to log.

### Literal LIKE searches

`EscapeLike` escapes `%`, `_` and backslash for LIKE/ILIKE with the default
backslash escape. Add intended wildcards afterwards and keep using parameters:

```go
pattern := "%" + pgfx.EscapeLike(search) + "%"
rows, err := db.FetchRows[Order](ctx, "SELECT id, amount FROM orders WHERE label ILIKE $1", pattern)
```

This fragment assumes `ctx`, `db`, `search` and an `Order` row model. The helper
is not SQL quoting, does not escape regular expressions or SIMILAR TO, and does
not support a different ESCAPE character.

## Testing

Import `pgfxtest "github.com/uchaloop/pgfx/test"`. Its
`MakeDB(ctx, opts ...pgfx.Option)` reads test configuration, creates a pool and
pings it before returning. A failed Ping closes the pool. The caller supplies a
deadline and owns a successful DB:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
db, err := pgfxtest.MakeDB(ctx)
if err != nil {
    t.Fatal(err)
}
t.Cleanup(db.Close)
```

Use this inside a `func TestQuery(t *testing.T)`, with `testing`, `context`,
`time` and `pgfxtest` imported. See the [complete connection example](test/example_test.go).
`PGFX_TEST_HOST` and `PGFX_TEST_DATABASE` are required; optional variables are
`PGFX_TEST_USER`, `PGFX_TEST_PASSWORD` and `PGFX_TEST_TLS_MODE` (default `disable`
for a disposable local database). Set an appropriate TLS mode or pass `WithTLS`
for other environments. The helper does not read `POSTGRES_*`; pgx's other libpq
defaults remain, including PGUSER/PGPASSWORD/.pgpass for empty credentials.
Set test credentials explicitly when that fallback is not desired.
`PGFX_TEST_MAX_CONNS` optionally limits the pool size. Empty or zero keeps the
pgxpool default; positive int32 values set the limit. Invalid or negative values
fail before connecting. Use `t.Setenv("PGFX_TEST_MAX_CONNS", "1")` before
`MakeDB` when a test requires one connection; such tests must not run in parallel.

No database creation, migrations, data isolation or automatic test skipping is
performed. Tests sharing a database must coordinate their fixtures. No container
runtime or assertion library is required. All pgfx options, including type
registration hooks, are supported. See [CONTRIBUTING](CONTRIBUTING.md) for the
local PostgreSQL setup and [test package GoDoc](test/db.go) for the contract.

## Reference

- [Package API](https://pkg.go.dev/github.com/uchaloop/pgfx) and [page API](https://pkg.go.dev/github.com/uchaloop/pgfx/page) for indexed releases
- Current source documentation: [pgfx](doc.go), [page](page/doc.go), [test](test/db.go)
- Examples: [standalone](examples/quickstart/main.go), [Fx](examples/fx/main.go), [fetches and transactions](example_test.go), [pagination](page/example_test.go)
- More examples: [optional rows and buffers](query_example_test.go), [transaction values](transaction_value_example_test.go), [error helpers and LIKE](helpers_example_test.go)
- [Changelog](CHANGELOG.md)
- [Contributing and PostgreSQL checks](CONTRIBUTING.md)

## Acknowledgements

Thanks to the authors of [pgx](https://github.com/jackc/pgx),
[Uber Fx](https://github.com/uber-go/fx), and
[otelpgx](https://github.com/exaring/otelpgx).

## License

[MIT](LICENSE)
