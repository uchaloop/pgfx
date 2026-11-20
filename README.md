# pgfx

<p align="center">
  <img src="logo.svg" alt="pgfx - Go gopher with elephant ears beside the pgfx wordmark" width="480">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg)](https://pkg.go.dev/github.com/uchaloop/pgfx) [![CI](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg)](https://github.com/uchaloop/pgfx/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/github/license/uchaloop/pgfx)](LICENSE)

**pgfx is a thin client over [pgx](https://github.com/jackc/pgx) with
[Uber Fx](https://github.com/uber-go/fx) integration and typed query helpers.**
It provides database connections through DI and adds transactions, pagination,
error classification, observability and optional availability policies.
The `shard` package provides application-side routing through fixed buckets.
SQL stays explicit: `DB` embeds `*pgxpool.Pool`, and `Tx` embeds `pgx.Tx`, so
native queries, batches, COPY and connection acquisition remain available.

[Quick start](#quick-start-with-fx) · [Queries](#queries) · [Transactions](#transactions) · [Pagination](#pagination) · [Availability](#availability) · [Sharding](#sharding) · [Reference](#reference)

## Installation

Requires Go 1.27.0 or later; see [go.mod](go.mod).

```sh
go get github.com/uchaloop/pgfx
```

This README describes the current checkout. For a published version, use the
README at its tag. The API may change between 0.x releases.

## Quick start with Fx

Supply a `Config`, register `MakeModule()` and inject `*pgfx.DB` into a consumer.
This complete program reads connection settings from the environment, prints
`1` after startup, then serves the Fx lifecycle until interrupted. It needs
an existing PostgreSQL database, but no application tables.

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/secret/v2"
	"go.uber.org/fx"
)

func main() {
	cfg := pgfx.Config{
		Host:     os.Getenv("POSTGRES_HOST"),
		Database: os.Getenv("POSTGRES_DATABASE"),
		User:     os.Getenv("POSTGRES_USER"),
		Password: secret.New(os.Getenv("POSTGRES_PASSWORD")),
		TLS: pgfx.TLSConfig{
			Mode:     os.Getenv("POSTGRES_TLS_MODE"),
			RootCert: os.Getenv("POSTGRES_TLS_ROOT_CERT"),
		},
	}

	fx.New(
		fx.Supply(cfg),
		pgfx.MakeModule(),
		fx.Invoke(registerQuery),
	).Run()
}

func registerQuery(lc fx.Lifecycle, db *pgfx.DB) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			value, err := db.FetchValue[int](ctx, "SELECT 1")
			if err != nil {
				return err
			}

			fmt.Println(value)

			return nil
		},
	})
}
```

Set `POSTGRES_HOST` and `POSTGRES_DATABASE`, and credentials if needed.
The example reads these variables explicitly; pgfx itself does not load ENV.
For remote servers, set TLS mode to `verify-full` with a trusted root certificate.
Use `disable` only for a trusted local test server. The default `prefer` mode
allows unencrypted fallback and does not verify the server's identity.

## Connections and configuration

Fx creates providers lazily: register a consumer to activate the connection.
Constructors may store `*DB`, but its pool is initialized only in `OnStart`.
Query in consumer start hooks or after startup; finish work in `OnStop`.
Fx closes the pool after its consumers stop. Do not close an injected pool yourself.

Startup validates configuration but does not wait for database availability.
Use `pgfx.MakeModule(pgfx.WithStartupPing())` to fail startup if Ping fails.
A consumer that queries in `OnStart`, as above, can also fail startup.
Pools with `MinConns` may connect in the background even without startup Ping.

`Config` holds connection, TLS, pool, timeout, health and breaker settings.
`Option` values supply runtime hooks, tracers and overrides. Both Fx modules
accept the same options as `MakeDB`; explicit options override injected defaults.
See [Config](config.go) and [Option](options.go) for individual defaults.

> **Configuration loaders:** [confmaker](https://github.com/uchaloop/confmaker)
> can load `Config`, and [confx](https://github.com/uchaloop/confx) can provide it
> through Fx. Both are optional. `Config` exposes ENV tags and the `POSTGRES_`
> prefix for compatible loaders; consult their documentation for registration.

### Named connections

For a replica or another database, supply a named `Config` and use the same
name in `MakeModuleFor`. This fragment assumes `replicaCfg` is configured and
reuses `registerQuery` from Quick start:

```go
fx.Options(
	fx.Supply(fx.Annotate(replicaCfg, fx.ResultTags(`name:"replica"`))),
	pgfx.MakeModuleFor("replica"),
	fx.Invoke(fx.Annotate(registerQuery, fx.ParamTags(``, `name:"replica"`))),
)
```

Each named connection has independent options, health state and breaker.
It is also available in the unordered `group:"pgfx"` value group and carries
its name in `DB.Name()`. The native pool is not a separate Fx dependency;
access it through `DB.Pool`.

Without Fx, call `MakeDB(ctx, cfg, opts...)` and `defer db.Close()` after checking
the error. `WithStartupPing()` then pings before returning and closes the pool
on failure. `MakeDB` never starts the Fx health monitor.

`Timeouts.Connect` bounds connection establishment. `Statement` and `LockWait`
set PostgreSQL startup parameters. Behind a pooler that rejects these parameters,
leave them zero and configure server-side limits instead. An operation context
deadline does not guarantee immediate cancellation of work on the server.
See [TimeoutConfig](config.go) for details.

## Queries

Use the same typed fetch helpers on `DB` and `Tx`:

| Method | Result |
| --- | --- |
| `FetchRows[T]` | Struct rows as `[]T` |
| `FetchRow[T]` | Exactly one struct row |
| `FetchOptionalRow[T]` | At most one struct row: `(T, isFound, error)` |
| `FetchValues[T]` | A single column as `[]T` |
| `FetchValue[T]` | Exactly one scalar value |
| `FetchRowsInto[T]`, `FetchValuesInto[T]` | Append results to a supplied slice |

Struct fields map by `db` tags, falling back to field names. JSON/JSONB values
use pgx codecs. Strict single-row methods return `pgx.ErrNoRows` for no rows and
`pgx.ErrTooManyRows` for more than one; optional reads return `false, nil` only
for an empty successful query. Query and decoding failures remain errors.

The following helpers assume an injected `db`, an operation `ctx` and an
`items` table with `id bigint`, `name text` and `price bigint`. They use imports
from the standard library, `pgx`, `pgfx` and `pgfx/page` as shown by their names.

```go
type Item struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Price int64  `db:"price"`
}

func listItems(ctx context.Context, db *pgfx.DB) ([]Item, error) {
	const query = `
		SELECT
			id,
			name,
			price
		FROM items
		ORDER BY id
	`

	return db.FetchRows[Item](ctx, query)
}

func findItem(ctx context.Context, db *pgfx.DB, id int64) (Item, bool, error) {
	const query = `
		SELECT
			id,
			name,
			price
		FROM items
		WHERE id = $1
	`

	return db.FetchOptionalRow[Item](ctx, query, id)
}
```

For `INSERT ... RETURNING id`, use `FetchValue[int64]`; for multiple returned
columns, use `FetchRow[Item]`. Always bind values as query parameters.

For empty results, `FetchRows` and `FetchValues` return non-nil empty slices.
The `Into` variants
append to the supplied slice: pass `dst[:0]` to reuse its buffer, and always use
the returned slice. Finish consuming old results before reuse. On error they
return nil, but the backing array may already contain writes.
See the [buffer examples](query_example_test.go).

### Literal LIKE searches

Escape user text before adding the wildcards you intend:

```go
func searchItems(ctx context.Context, db *pgfx.DB, text string) ([]Item, error) {
	pattern := "%" + pgfx.EscapeLike(text) + "%"

	const query = `
		SELECT
			id,
			name,
			price
		FROM items
		WHERE name ILIKE $1
		ORDER BY id
	`

	return db.FetchRows[Item](ctx, query, pattern)
}
```

`EscapeLike` escapes `%`, `_` and backslash for PostgreSQL's default LIKE escape.
It is not SQL quoting or regular-expression escaping; the pattern still needs
to be passed as a parameter.

## Transactions

`Transaction` commits on a nil callback error and rolls back on an error or
panic; a panic continues unwinding. Use the callback's `Tx` for every operation
in the transaction. Calling the outer `DB` uses the pool instead.

`TransactionValue` returns the callback's value only after a successful commit;
any error returns a zero value. For example, using the `Item` above:

```go
func createItem(ctx context.Context, db *pgfx.DB, item Item) (int64, error) {
	const query = `
		INSERT INTO items (
			id,
			name,
			price
		)
		VALUES (
			$1,
			$2,
			$3
		)
		RETURNING
			id
	`

	return db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int64, error) {
		return tx.FetchValue[int64](
			ctx,
			query,
			item.ID,
			item.Name,
			item.Price,
		)
	})
}
```

`Tx.Transaction` and `Tx.TransactionValue` use nested savepoints; success does
not commit the outer transaction. `Tx.BeginNested` gives manual ownership of a
typed savepoint. See the [transaction examples](example_test.go).
A failed COMMIT can have an unknown outcome: retry only operations whose
application contract makes repetition safe.

## Pagination

Use a `page.Pager` to fetch numbered pages with a total count. Configure it once
for each list. Lists can share a size policy or use different minimum, maximum,
and default sizes.

```go
func makeItemPager() (page.Pager, error) {
	const minSize, maxSize, defaultSize = 1, 100, 20
	limits, err := page.MakeLimits(minSize, maxSize, defaultSize)
	if err != nil {
		return page.Pager{}, err
	}

	return page.MakePager(limits, page.SortConfig{
		Fields: page.SortFields{"name": "i.name", "id": "i.id"},
		Tie:    "i.id",
	})
}
```

For each request, resolve the page number, size, and sort values. Pass the
selection to `db.FetchPage` or `tx.FetchPage`. The query must include one
`@order` marker, `@limit`, and `@offset`. Pass filter values in
`pgx.NamedArgs`; pgfx supplies the three pagination parameters.

```go
type Item struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

func listItems(ctx context.Context, db *pgfx.DB, pager page.Pager,
	number, size int, sort []string, category string,
) (page.Result[Item], error) {
	selection, err := pager.Resolve(number, size, sort)
	if err != nil {
		return page.Result[Item]{}, err
	}

	const query = `
		SELECT i.id, i.name, count(*) OVER () AS total
		FROM items AS i
		WHERE i.category = @category
		ORDER BY @order
		LIMIT @limit OFFSET @offset
	`

	return db.FetchPage[Item](
		ctx,
		query,
		pgx.NamedArgs{"category": category},
		selection,
	)
}
```

Page numbers start at 1. A number below 1 selects the first page. A size below
1 selects the default; positive sizes are clamped to the configured range.
An offset that overflows returns `page.ErrBounds`. To enforce a ceiling for one
list, use the copy returned by `limits.WithMaxOffset(maxOffset)` when creating
its pager. Offsets above the ceiling return `page.ErrBounds` before querying.
Choose the ceiling and a request deadline for your workload. Pass the same
deadline-bearing context through fallback and both page reads. A ceiling
limits depth; the exact count can still scan all matching rows.

### Sorting

For page 2, size 10, and `[]string{"name:desc"}`, the example orders by
`i.name DESC, i.id DESC` and skips 10 rows. Configure public field names with
trusted, application-defined SQL expressions and choose a trusted unique tie column so rows with equal
names have a consistent order.

`Resolve` uses the first recognized field, ignoring case and surrounding
spaces. Only `desc` selects descending order. With no recognized field, the
pager orders by the tie column ascending.

### Results and query cost

`Result.List` contains the page rows; `Result.Total` counts all matching rows.
Return a final `bigint` column named `total`, usually `count(*) OVER () AS total`.
If the query needs distinct items, deduplicate before computing the window count.
FetchPage returns an error and discards the partial list if the query returns more rows than requested. A full count and a deep offset can both be expensive.

An empty page has a non-nil list. If a positive offset yields no rows, pgfx
repeats the query with limit 1 and offset 0 to read the total. Use a repeatable-read
transaction when both reads must see the same snapshot.

### Queries with their own order

For a fixed SQL order such as `ORDER BY id`, pass `page.Bounds` directly to
`FetchPage` and use only `@limit` and `@offset` for pagination.
`page.MakeBounds(number, size)` requires positive inputs and applies no size cap.
Use `limits.Bounds(number, size)` to apply your configured defaults and limits.
For explicit multi-column sorting, see [Sortable.OrderBy](page/sort.go) and the
[query example](query_example_test.go).

## Errors

pgfx preserves PostgreSQL errors. Use `errors.Is` for sentinel errors and these
helpers for server errors:

| Helper | Purpose |
| --- | --- |
| `AsPgError`, `SQLState` | Inspect the first `*pgconn.PgError` in an error tree |
| `IsSQLState` | Match a nonempty SQLSTATE |
| `IsConstraint` | Match SQLSTATE and constraint name on that same error |
| `IsUniqueViolation`, `IsForeignKeyViolation`, other predicates | Common constraint and SQL failures |
| `IsSerializationFailure`, `IsDeadlockDetected` | Identify transaction failures; no automatic retry |

For a query against a known table, a fragment in an error-returning function:

```go
if pgfx.IsConstraint(err, "23505", "items_pkey") {
	return errors.New("item already exists")
}
return err
```

Constraint names are not globally unique; use the query's schema/table context.
Classification follows the first PostgreSQL error, not every member of
`errors.Join`, and never establishes that retrying is safe. Do not log raw
connection errors or SQL containing secrets. See [all predicates](errors.go).

## Hooks and observability

Pass runtime options directly to `MakeModule` or `MakeModuleFor`.
For example, register custom PostgreSQL types on every new physical connection:

```go
func registerTypes(ctx context.Context, conn *pgx.Conn) error {
	typ, err := conn.LoadType(ctx, "public.item_status")
	if err != nil {
		return err
	}

	conn.TypeMap().RegisterType(typ)

	return nil
}

// Use as an option in fx.New alongside the Config provider and DB consumer:
// pgfx.MakeModule(pgfx.WithAfterConnect(registerTypes))
```

Create the database type before startup. Register arrays too when needed.
Hooks must support concurrent calls on different connections, honor supplied
contexts and finish promptly. Do not retain a borrowed connection or re-enter
its pool. Repeating a hook option replaces that hook; nil clears it.

| Options | Purpose |
| --- | --- |
| `WithBeforeConnect`, `WithAfterConnect` | Credentials and connection initialization |
| `WithPrepareConn`, `WithAfterRelease` | Check a borrowed connection or decide whether to retain it |
| `WithBeforeClose`, `WithShouldPing` | Cleanup and the pool's acquisition-time Ping policy |
| `WithTLS` | Supply an in-memory TLS configuration instead of file settings |
| `WithDefaultQueryExecMode`, `WithStatementCacheCapacity` | Configure native pgx execution |
| `WithRuntimeParam` | Set a PostgreSQL startup parameter |

`WithTLS` removes configured TLS/plaintext fallbacks; nil disables TLS.
`WithShouldPing` does not suppress explicit startup or health checks.
See [hook outcomes and ownership](options.go) before implementing pool policies.

### Tracing and metrics

Fx enables otelpgx tracing when a `trace.TracerProvider` is injected.
`WithTrace` configures otelpgx explicitly, and `WithTracer` adds a custom tracer;
tracers and metrics compose. Supply `pgfx.QueryMetricFunc` through Fx, or use
`WithQueryMetrics` directly. This fragment assumes a concurrency-safe,
non-blocking `recordMetric` callback:

```go
fx.Options(
	fx.Supply(pgfx.QueryMetricFunc(recordMetric)),
	pgfx.MakeModule(),
)
```

Tag calls with `pgfx.WithQueryName(ctx, "items.list")`. Metrics cover query,
batch and COPY trace lifecycles; `Connection` identifies the named pool.
Use bounded names as labels. SQL is omitted unless `WithSQLInQueryMetrics(true)`
is enabled and may contain sensitive literals. Metrics are not proof of commit
or a complete audit of failed acquisitions; see [QueryMetricFunc](metrics.go).

## Availability

These mechanisms solve different problems and are optional:

| Mechanism | Effect |
| --- | --- |
| `ReadWithFallback` | Try another connection for an availability failure |
| `Config.Breaker` | Refuse attempts on a repeatedly failing connection |
| `Config.Health` | Request shutdown of the whole Fx application after sustained unavailability |

### Read fallback

This function assumes `dbs` is an ordered list, such as master then replica:

```go
func findWithFallback(ctx context.Context, dbs []*pgfx.DB, id int64) (Item, error) {
	return pgfx.ReadWithFallback(ctx, dbs, func(ctx context.Context, db *pgfx.DB) (Item, error) {
		const query = `
			SELECT
				id,
				name,
				price
			FROM items
			WHERE id = $1
		`

		return db.FetchRow[Item](ctx, query, id)
	})
}
```

Callbacks must be repeatable reads without writes or external side effects.
Consume results before returning; do not return live `Rows` or `Tx`.
Fallback follows `IsUnavailable`, not every error: statement/decoding errors,
missing rows and ordinary operation deadlines do not trigger it. A connection
establishment timeout may allow fallback while the caller's context is live.
All attempts share that context; replicas may lag. See [ReadWithFallback](fallback.go).

### Connection breaker

Set `Config.Breaker`, for example `BreakerConfig{Failures: 5, OpenFor: 5*time.Second}`.
The breaker belongs to one connection in one process. Helpers in `shard` use it
automatically; for direct access, request a permit explicitly:

```go
func pingWithBreaker(ctx context.Context, db *pgfx.DB) error {
	ctx, attempt, err := db.Breaker().Attempt(ctx)
	if err != nil {
		return err
	}
	defer attempt.Release()

	err = db.Ping(ctx)
	attempt.Done(err)
	if err != nil {
		return err
	}

	return nil
}
```

Repeated failures open the breaker; after `OpenFor`, one probe can test recovery.
Only the first `Done` or `Release` counts. `Release` is neutral, including during
panic. `Done(nil)` must mean a completed database operation, not validation alone.
The returned context suppresses duplicate tracer accounting for that breaker.
Direct pool calls are still allowed while open: they do not enforce the permit.
Outside an attempt, tracing accounts for individual commands, not whole transactions.
A disabled breaker grants attempts without recording them. See [Breaker](breaker.go)
for error classification, nested attempts and probe lifetime.

### Health shutdown

Enable health only when loss of that database should stop the whole application.
For independent shards, prefer their breakers. This fragment assumes `cfg` is
the connection configuration and `registerQuery` is the Quick start consumer:

```go
cfg.Health = pgfx.HealthConfig{
	IsEnabled:    true,
	Interval:     5 * time.Second,
	Timeout:      time.Second,
	FailureAfter: 30 * time.Second,
}

fx.Options(
	fx.Supply(cfg),
	pgfx.HealthModule,
	pgfx.MakeModule(pgfx.WithHealthSQLStates(pgfx.DefaultHealthSQLStates()...)),
	fx.Invoke(registerQuery),
)
```

Place `HealthModule` once at the application root, not inside another module.
All durations are required: `Timeout <= Interval <= FailureAfter`.
SQLSTATE codes are opt-in; pass the default list explicitly or supply your own.
Transport failures and check timeouts count independently, including pool
saturation. Successful or ignored checks reset the failure window. Checks are
sampled; shutdown is not an exact deadline. Startup Ping is not filtered.

Health requests exit code 1. `fx.App.Run` handles the shutdown lifecycle; manual
Start/Stop owners must handle `app.Wait()` and propagate its exit code.
Logs use an injected `*slog.Logger` or `slog.Default()` without raw errors.
See [HealthConfig](health.go) and the [complete health example](examples/health/main.go).

## Sharding

`pgfx/shard` maps keys to fixed buckets and assigns bucket ranges to named
connections. Use `ModuloBuckets[int64](1024)` for numeric keys, `StringBuckets`
for stable string hashing, or `MakeBuckets` for a deterministic custom formula.
Keep the formula and bucket count unchanged after data is written.

Supply a `shard.LayoutConfig` with connection names and complete, non-overlapping
ranges. Named `MakeModuleFor` connections are already in `group:"pgfx"`.
This provider fragment assumes `layoutCfg` is supplied by your application:

```go
fx.Provide(fx.Annotate(
	func(dbs []*pgfx.DB) (*shard.Router[int64], error) {
		layout, err := shard.MakeLayout(layoutCfg)
		if err != nil {
			return nil, err
		}

		return shard.MakeRouter(shard.ModuloBuckets[int64](layoutCfg.Buckets), layout, dbs)
	},
	fx.ParamTags(`group:"pgfx"`),
))
```

Enable `Config.Breaker` on every referenced connection and register a router
consumer. Connection names select destinations, not the order of the Fx group.
`LayoutConfig` supports text forms such as `a:a-master|a-replica` for nodes,
`a:0-511` for active ranges and `a>b:0-511` while moving a range.

| Router operation | Use |
| --- | --- |
| `Write(ctx, key, fn)` | Run a callback on every write destination inside its attempt |
| `Read(ctx, key, fn)` | Read the source master, then its replicas on availability failure |
| `Partition(items, key)` | Group original input by write destination; retain `Indexes` and layout `Version` |
| `Each(ctx, limit, fn)` | Visit shards with bounded concurrency; callback chooses `Shard.Read` or `Target.Attempt` |
| `Route`, `Shards`, `Version` | Inspect one local snapshot or obtain targets for manual operations |
| `Update` | Validate a transition and atomically replace the local layout with a newer version |

`Write` can partially succeed: inspect per-shard results even when it returns
an error. Cross-shard writes are not atomic. `Partition` only groups data;
it neither executes SQL nor retries, and keeps duplicate keys. Its item copies
are shallow. Collapse duplicates appropriately before a batched upsert;
maximum version is valid for full snapshots, not increments.

A move follows active A -> migrating A>B -> active B; rollback to A is allowed.
While migrating, writes go to both and reads use the source. Every write must be
safe to retry; concurrent full snapshots need comparable versions, a rule for
equal versions and versioned deletes. Copying must follow the same rules.
The application coordinates all writers, finishes old-version work or fences
stale writes before cutover, and copies/verifies/cleans up data. `Update` does
not perform these steps. Routing is not an authorization check.

See the [complete Fx and transaction examples](examples/sharding/README.md),
the [routing example](shard/example_test.go) and [package contracts](shard/doc.go).
Replica fallback does not support queue processing with row locks or deletion.

## Testing

Import `pgfx/test` as `pgfxtest` to connect tests to an existing PostgreSQL:

```go
func TestDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	value, err := db.FetchValue[int](ctx, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}

	if value != 1 {
		t.Fatalf("value = %d", value)
	}
}
```

Set `PGFX_TEST_HOST` and `PGFX_TEST_DATABASE`; optional variables include
`PGFX_TEST_USER`, `PGFX_TEST_PASSWORD`, `PGFX_TEST_TLS_MODE` and
`PGFX_TEST_MAX_CONNS`. TLS defaults to disabled for local tests.
`MakeDB` verifies readiness with Ping. The helper does not create databases,
isolate data, migrate schemas or skip tests. Use `pgfxtest.Config()` when you
need to adjust configuration before creating the pool. See [test package](test/db.go).

## Reference

- [API reference](https://pkg.go.dev/github.com/uchaloop/pgfx), [page](https://pkg.go.dev/github.com/uchaloop/pgfx/page) and [test](https://pkg.go.dev/github.com/uchaloop/pgfx/test) for published versions.
- Current source: [package overview](doc.go), [shard API](shard/doc.go), [query examples](query_example_test.go), [transaction examples](transaction_value_example_test.go).
- [Contributing and local checks](CONTRIBUTING.md) · [Changelog](CHANGELOG.md) · [MIT license](LICENSE).
