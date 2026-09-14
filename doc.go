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

[Make] creates a pool owned by the caller, who must close it. Pool creation does
not verify connectivity; use Ping when startup verification is required.
[Module] supplies *DB through Uber Fx, verifies connectivity on application
start and closes the pool on stop:

	fx.New(
		fx.Supply(pgfx.Config{Host: "localhost:5432", Database: "orders"}),
		pgfx.Module,
	).Run()

[ModuleFor] consumes a Config with an Fx name and provides a DB with the same
name. Each named connection has its own configuration. The native pool is not
registered separately in Fx; it is available through DB.Pool.

For convenient environment loading and Fx integration, confmaker/confx is an
optional recommended loader. It can replace manual Config construction; it is
not required by pgfx. See the README for its setup and environment prefixes.

# Typed fetches

[DB.FetchRows] and [DB.FetchRow] decode struct fields using db tags, falling back
to field names. JSON/JSONB columns use pgx codecs, including map fields.
[DB.FetchValues] and [DB.FetchValue] decode a single column into scalar values.
The same methods are available on Tx.

Singular fetches require exactly one row and return pgx.ErrNoRows or
pgx.ErrTooManyRows otherwise. Plural fetches accept an empty result. Use
FetchValue for a single-column INSERT or UPDATE with RETURNING, and FetchRow
for multiple returned columns. Native pgx operations remain available directly.

# Pagination

[DB.FetchPage] returns a list and total: the total number of rows matching the
base SELECT before pagination. [DB.FetchPageRows] returns the list and whether
another page follows, and never executes a count query. Both methods decode
structs using the same db mapping as FetchRows and are also available on Tx.

Build a [page.Query] once from a SELECT containing columns, joins and filters.
Do not add top-level ORDER BY, LIMIT or pagination to that SELECT. Pass filter
arguments separately from the [page.Request], starting at $1.

Numbered pages use LIMIT/OFFSET and read one row past the page, which tells
whether another page follows. [DB.FetchAfter] instead accepts a
[page.CursorRequest] and returns a [page.CursorResult] with List, NextCursor and
HasMore. It seeks after the last returned sort keys and fetches one extra row;
it never counts. [DB.FetchTotal] explicitly counts the base filter independently.
These methods are also available on Tx. Prefer offset for shallow numbered
pages and cursor for sequential traversal of large results with matching indexes.

Cursor keys must be selected output columns. Tokens bind SQL, sort and filter
arguments, which must have stable JSON representations. Tokens are not signed
or encrypted and do not replace authorization. Supported key types are documented
on [page.CursorStatement.Cursor]; arbitrary custom pgx types are not supported.
Changing sort keys concurrently can still move rows across page boundaries.

Sorting applies [page.Head], the requested sort, then [page.Tie]. Tie must
provide a unique ordering for the result; after other orders it takes the
direction of the last one, so one composite index serves the whole order. A
client sorts only by the fields [page.Sortable] lists; [page.SortKeyTag] opens
every field of the model under the names of a struct tag, such as json. Unknown
sort fields return an error.

FetchPage counts the base SELECT in a separate statement unless a nonempty page
with no row after it already determines the total. A page that more rows follow,
and an empty page, require the count. [page.CountSQL] can supply a cheaper
equivalent SELECT for counting; it
must return the same number of rows and accept the same arguments. JOIN
multiplicity, DISTINCT and GROUP BY belong to the base SELECT: pgfx counts its
result rows, not inferred entities.

Pagination does not create a transaction. Concurrent writes can change data
between the list and count statements. When one snapshot is required, call
[Tx.FetchPage] inside [DB.Transaction] with pgx.RepeatableRead and, for reads,
pgx.ReadOnly. This is an explicit caller choice.

# Transactions

[DB.Transaction] commits when the callback returns nil and rolls back on an
error or panic. Its callback receives a typed Tx. Use that Tx for every operation
that belongs to the transaction; calls through DB use the pool instead.
[Tx.Transaction] provides the same callback style for a nested savepoint.
[Tx.BeginNested] returns a typed savepoint when manual ownership is needed.
Isolation and access mode are supplied through pgx.TxOptions.

# Tracing and metrics

[WithTracing] enables otelpgx spans; [WithTracer] installs a custom pgx tracer.
[Module] enables tracing when a trace.TracerProvider is supplied through Fx.
[WithQueryMetrics] installs a callback without requiring a metrics backend.
The callback must be concurrency-safe, cheap and non-blocking.

[WithQueryName] gives an operation a stable name. A pagination count uses that
name with a .count suffix. SQL is omitted from metrics unless explicitly enabled
with [WithSQLInQueryMetrics]. Use bounded names, not raw SQL, as metric labels.

Metrics follow pgx hooks. Duration includes result consumption but excludes pool
acquisition. A batch produces one observation, with potentially partial row
counts on error; it does not imply commit. Empty batches and pool acquisition
failures are not observed. Some early COPY failures omit the end hook. See
[QueryMetric] and [QueryMetricFunc] for the full callback contract.

# TLS and connection hooks

[TLSConfig] supplies file-based TLS settings. [WithTLS] supplies an in-memory
configuration and replaces the configured fallbacks. [WithBeforeConnect] and
[WithAfterConnect] expose connection hooks for credentials and type registration.
*/
package pgfx
