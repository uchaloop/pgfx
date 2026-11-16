/*
Package pgfx is a thin client that extends pgx/pgxpool with typed fetch methods,
pagination, transaction callbacks, configuration and optional observability.
It keeps SQL explicit and preserves the native pgx API: [DB] embeds
*pgxpool.Pool and [Tx] embeds pgx.Tx. It is not an ORM or a replacement driver.

# Configuration

[Config] contains connection data; [Option] supplies runtime dependencies such
as tracers, callbacks and an in-memory TLS configuration. The application
constructs Config in Go or uses a loader of its choice. pgfx does not read a
configuration source itself. Host and Database are required; optional fields
retain their documented pgx defaults when unset.

[MakeDB] creates a pool owned by the caller, who must close it. Pool creation does
not verify connectivity; use Ping when startup verification is required.
[MakeModule] supplies *Connection through Uber Fx. It creates and pings the pool
on start and closes it on stop. Consumers call [Connection.DB] in their own
OnStart hooks or after successful startup. Before readiness and after shutdown,
the handle returns [ErrConnectionNotReady] or [ErrConnectionClosed], respectively.

Fx constructs providers lazily: register a consumer of *Connection to activate
its lifecycle hooks. The runnable examples/fx program registers a consumer,
executes SELECT 1 in its OnStart hook, and stops the application. The examples
for MakeModule and MakeModuleFor show untagged and named consumers.

[MakeModuleFor] consumes a Config with an Fx name and provides a Connection with the same
name. Each named connection has its own configuration and runtime options.
Both modules accept the same [Option] values as [MakeDB], applied after injected
tracing and metrics defaults. Use [WithAfterConnect] for per-connection type
registration; create the database types before application startup. The native
pool is not registered separately in Fx; it is available through DB.Pool.

# Optional health policy

[HealthConfig] enables periodic checks only for Fx-managed connections. Add
[HealthModule] once at the application root; do not nest it inside fx.Module or
bypass its decorated shutdowner. Enabled checks without it fail construction
with [ErrHealthModuleRequired]. Both the policy and adapter are optional when
checks are disabled. [MakeDB] validates the policy but never runs a monitor.

[WithHealthSQLStates] selects the SQLSTATE errors counted by background checks.
No codes are implicit; pass [DefaultHealthSQLStates]() for the recommended list.
Transport errors without SQLSTATE and check timeouts count independently.
Success or an ignored error resets the failure window. Startup Ping is not filtered.
Each named connection uses its own policy and independent failure window. Sustained pool unavailability,
including saturation, can stop the whole application. See [HealthConfig] for
explicit duration requirements, recovery and sampled failure timing, and
[HealthModule] for the shared shutdown contract.

An optional *slog.Logger controls transition logs; otherwise slog.Default is
used. Logs omit raw errors and credentials. Logging callbacks must not block.
OnStop cancels and joins the monitor before closing the pool; consumer stop
hooks run first in Fx's reverse lifecycle order and must release borrowed
connections. fx.App.Run handles shutdown; manual Start/Stop owners must receive
app.Wait, stop the application and propagate the exit code themselves.

# Typed fetches

[DB.FetchRows] and [DB.FetchRow] decode struct fields using db tags, falling back
to field names. JSON/JSONB columns use pgx codecs, including map fields.
[DB.FetchValues] and [DB.FetchValue] decode a single column into scalar values.
The same methods are available on Tx.

[DB.FetchRowsInto] and [DB.FetchValuesInto] append to a caller-provided slice,
preserving existing elements and growing its capacity when necessary. Pass a
zero-length slice with capacity to reserve memory, or dst[:0] to reuse a buffer
once its previous consumers have finished. Always use the returned slice. Errors
return nil but may leave writes in the supplied backing array. The same methods
are available on Tx.

[DB.FetchRow] and [DB.FetchValue], and their Tx counterparts, require exactly
one row: an empty result returns pgx.ErrNoRows, and multiple rows return
pgx.ErrTooManyRows. Query and decoding errors remain errors. Plural fetches
accept an empty result. Use
FetchValue for a single-column INSERT or UPDATE with RETURNING, and FetchRow
for multiple returned columns. Native pgx operations remain available directly.

# Pagination

[github.com/uchaloop/pgfx/page.Fetch] combines list and count callbacks into [github.com/uchaloop/pgfx/page.Result], with List
and Total (int64). By default it calls list then count, even for an empty list.
A callback error returns a zero result; a successful nil list becomes non-nil
and empty. The context and both callbacks must be non-nil.

[github.com/uchaloop/pgfx/page.WithParallel] explicitly runs both callbacks concurrently. Pass the callback
context to queries so cancellation reaches them. The first recorded callback
error cancels the sibling; Fetch waits for both callbacks before returning that
error with a zero result. Callbacks must cooperate with cancellation. Never run
parallel callbacks on the same transaction or connection. Pool-backed callbacks
can acquire separate connections; account for the additional pool demand.

The application owns SQL, filter arguments, request validation, maximum page
size, offset arithmetic and sort whitelists. Keep list and count filters
consistent, preserving DISTINCT, grouping and join semantics in the count.
Parameterize values and never interpolate raw client sort text. Use a unique
order for stable pages. Matching indexes can support ordering, but execution
plans depend on predicates and NULL placement. Deep offsets scan skipped rows
and an exact count covers the complete base result.

Pagination does not create a transaction. Concurrent writes can make the two
queries observe different snapshots, including in a READ COMMITTED transaction.
For one snapshot, use an explicit pgx.RepeatableRead transaction with sequential
page.Fetch callbacks that both query its Tx. Do not use page.WithParallel there.

# Transactions

[DB.Transaction] commits when the callback returns nil and rolls back on an
error or panic. Its callback receives a typed Tx. Use that Tx for every operation
that belongs to the transaction; calls through DB use the pool instead.
[Tx.Transaction] provides the same callback style for a nested savepoint.
[Tx.BeginNested] returns a typed savepoint when manual ownership is needed.
Isolation and access mode are supplied through pgx.TxOptions.

# Tracing and metrics

[WithTrace] enables otelpgx spans; [WithTracer] installs a custom pgx tracer.
[MakeModule] enables tracing when a trace.TracerProvider is supplied through Fx.
[WithQueryMetrics] installs a callback without requiring a metrics backend.
The callback must be concurrency-safe, cheap and non-blocking.

[WithQueryName] gives an operation a stable name.
SQL is omitted from metrics unless explicitly enabled
with [WithSQLInQueryMetrics]. Use bounded names, not raw SQL, as metric labels.

See [QueryMetric] and [QueryMetricFunc] for measurement boundaries, batch
aggregation, partial results and pgx hook coverage limitations.

# Optional rows

DB.FetchOptionalRow and Tx.FetchOptionalRow return a value, an isFound flag and
an error. Only an empty successful query returns false with no error; FetchRow
continues to require exactly one row. IsConstraint matches a nonempty SQLSTATE
and constraint name on the same first PostgreSQL error.

# Transactions with values

DB.TransactionValue returns a typed callback result after successful commit.
Tx.TransactionValue releases a savepoint, without committing the outer transaction.
Both return a zero value on error and preserve Transaction error and panic semantics.

# Error classification and test support

[AsPgError], [SQLState] and [IsSQLState] classify the first PostgreSQL error in an
error tree without replacing it. Constraint and transaction-error predicates
provide common checks; they do not establish that retrying is safe.
[EscapeLike] escapes literal text for parameterized LIKE/ILIKE patterns with
PostgreSQL's default backslash escape, not SQL or regular expressions.

The github.com/uchaloop/pgfx/test package connects tests to an existing database
and verifies readiness with Ping. It adds no isolation or automatic skipping.

# TLS and connection hooks

[TLSConfig] supplies file-based TLS settings. [WithTLS] supplies an in-memory
configuration and replaces the configured fallbacks. [WithBeforeConnect] and
[WithAfterConnect] expose connection hooks for credentials and type registration.
[WithPrepareConn], [WithAfterRelease], [WithBeforeClose] and [WithShouldPing]
configure the native pool lifecycle callbacks through MakeDB, MakeModule or MakeModuleFor.
See their GoDoc for callback outcomes, concurrency and shutdown requirements.
*/
package pgfx
