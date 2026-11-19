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

[Config.EnvPrefix] declares POSTGRES_ for compatible loaders without adding a
loader dependency. The updated confmaker built-in engine can infer a default
instance from it; confx.Provide[Config]() supplies that configuration untagged.
Named master and replica registrations read MASTER_POSTGRES_* and
REPLICA_POSTGRES_* while keeping their Fx names. The explicit name postgres
still reads POSTGRES_*. A loader WithPrefix override replaces the whole prefix,
so WithPrefix("MASTER_") preserves legacy master variables. Deploy renamed
variables with the corresponding binary; pgfx never reads them itself.

[MakeDB] creates a pool owned by the caller, who must close it. Pool creation does
not verify connectivity; pass [WithStartupPing] or call Ping when startup
verification is required.
[MakeModule] supplies *DB through Uber Fx. It creates the pool on start and
closes it on stop. Start does not wait for the server and does not fail because
it is unavailable; [WithStartupPing] restores a failing start. Consumers use DB directly in their own
OnStart hooks or after successful startup. Constructors only store the pointer:
the embedded Pool is nil until startup succeeds. Consumers finish their work
in OnStop before the database hook closes the pool. They must not close it themselves.

Fx constructs providers lazily: register a consumer of *DB to activate
its lifecycle hooks. The runnable examples/fx program registers a consumer,
executes SELECT 1 in its OnStart hook, and stops the application. The examples
for MakeModule and MakeModuleFor show untagged and named consumers.

[MakeModuleFor] consumes a Config with an Fx name and provides a DB with the same
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

[DB.FetchOptionalRow] and [Tx.FetchOptionalRow] return a value, an isFound flag
and an error. Only an empty successful query returns false with no error;
FetchRow continues to require exactly one row.

# Pagination

[DB.FetchPage] and [Tx.FetchPage] read one numbered page and the number of
rows the query matches in a single statement. The query binds @limit and
@offset and ends with a total column, usually count(*) OVER () AS total.
Package [github.com/uchaloop/pgfx/page] converts a page number and size into
bounds and builds the ORDER BY list from a whitelist of SQL expressions.

# Connection breaker

Config.Breaker enables a per-connection breaker ([DB.Breaker]): consecutive
failed operations open it, and after a pause one probe operation decides
whether it closes. An operation runs between [Breaker.Attempt] and
[Attempt.Done]; queries outside an Attempt are counted one command at a time.
An open breaker refuses Attempt with [ErrBreakerOpen] but does not block
queries issued directly through DB. Without a breaker nothing is installed.

# Sharding

Package [github.com/uchaloop/pgfx/shard] routes keys to shards inside the
application: buckets, a text layout of shards and bucket ranges, transition
checks for moving ranges, and a router. Router.Write, Router.Read and Shard.Read
run database callbacks inside breaker attempts. Router.Each leaves attempt
management to its callback; Router.Partition only groups items and does not
acquire attempts. [MakeModuleFor] names each connection and adds it to the value
group `group:"pgfx"` that the router consumes.

# Read fallback

[ReadWithFallback] runs a fully consumed, side-effect-free read on a list of
databases in order, moving on only when an attempt fails with a qualifying
availability error ([IsUnavailable]). All attempts share the caller context. Wrap an entire paginated read to preserve its snapshot;
no existing transaction is transferred. See the function contract for error policy.

# Transactions

[DB.Transaction] commits when the callback returns nil and rolls back on an
error or panic. Its callback receives a typed Tx. Use that Tx for every operation
that belongs to the transaction; calls through DB use the pool instead.
[Tx.Transaction] provides the same callback style for a nested savepoint.
[Tx.BeginNested] returns a typed savepoint when manual ownership is needed.
Isolation and access mode are supplied through pgx.TxOptions.

[DB.TransactionValue] returns a typed callback result after successful commit.
[Tx.TransactionValue] releases a savepoint without committing the outer transaction.
Both return a zero value on error and preserve Transaction error and panic semantics.

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

# Error classification and test support

[AsPgError], [SQLState] and [IsSQLState] classify the first PostgreSQL error in an
error tree without replacing it. Constraint and transaction-error predicates
provide common checks; they do not establish that retrying is safe.
[IsConstraint] matches a nonempty SQLSTATE and constraint name on the same
first PostgreSQL error.
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
