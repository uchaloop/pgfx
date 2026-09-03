/*
Package pgfx is a thin, Fx-first layer over pgx/pgxpool for Postgres. A pgfx
connection is a [DB]: a pool built from a plain, serializable [Config], carrying
generic methods for the two things every query does - read rows, read one value.

# Config and Option

The split between the two is the design. [Config] is data: it survives a round
trip through text, so a deployment fills it and a manifest lists it. Everything
that cannot survive that trip - a tracer, a metrics callback, a pool hook, an
in-memory *tls.Config - is an [Option] instead. Nothing is configured twice, and
nothing has to be expressed as a string that was never a string.

pgfx reads no configuration source of itself, which is what keeps it decoupled
from any particular loader. An application supplies the Config, typically
through github.com/uchaloop/confmaker, whose Manifest lists every variable a
connection reads:

	confx.Manifest[pgfx.Config]("postgres")

Host and Database have to be supplied. An empty User or Password falls through
to pgx's libpq-compatible defaults, so peer authentication and a .pgpass keep
working. Every pool and timeout field left at its zero value keeps the pgxpool
default, so a deployment sets what it means to change and nothing else.

# Wiring

[Module] provides an untagged *[DB] - the single default connection. It verifies
the connection while the application starts, so a bad endpoint fails the start
rather than the first query, and closes the pool on shutdown. Fx receives and
provides only *DB; the embedded *pgxpool.Pool is not registered separately.

[ModuleFor] adds a replica or a shard under a name, which gives both the Fx tag
and the environment prefix:

	fx.New(
		confx.Module(),
		confx.Provide[pgfx.Config]("postgres"),
		confx.ProvideNamed[pgfx.Config]("replica"),

		pgfx.Module,
		pgfx.ModuleFor("replica"),
	)

A named connection inherits nothing from the default one. Its database, user and
TLS settings are given again under its own prefix - a replica that silently
borrowed the primary's credentials would be the kind of thing noticed in
production.

Several databases are several named connections; there is no other mechanism.
Without Fx, [Make] builds a DB from a Config filled by hand.

# Queries

FetchRow and FetchRows decode structs by `db` tag or field name:

	order, err := db.FetchRow[Order](
		ctx,
		`SELECT id, amount FROM orders WHERE id = $1`, id,
	)

	orders, err := db.FetchRows[Order](
		ctx,
		`SELECT id, amount FROM orders ORDER BY id`,
	)

FetchValue and FetchValues decode one column:

	total, err := db.FetchValue[int64](ctx, `SELECT count(*) FROM orders`)

	ids, err := db.FetchValues[int64](
		ctx,
		`SELECT id FROM orders ORDER BY id`,
	)

The singular methods require exactly one row and report pgx.ErrNoRows or
pgx.ErrTooManyRows otherwise. pgx.ErrNoRows wraps sql.ErrNoRows, so errors.Is
matches either sentinel.

DB embeds *pgxpool.Pool. Native methods such as Exec, Query, CopyFrom, Acquire,
and Close are therefore available directly:

	tag, err := db.Exec(
		ctx,
		`UPDATE orders SET status = $1 WHERE id = $2`,
		"paid", id,
	)

Use FetchValue rather than Exec for INSERT ... RETURNING.

# Pagination

FetchPage returns one page of rows and how many rows the filter matches in
total. The query stays a plain SELECT - no ORDER BY, no LIMIT, no row numbers,
no count - and the page is applied around it, which is what keeps the filter
written once and the rows decodable by the `db` tag like any other:

	SELECT * FROM ( <query> ) AS pgfx_page ORDER BY ... LIMIT $n+1 OFFSET $n+2
	SELECT count(*) FROM ( <query> ) AS pgfx_page

The sort policy is bound once, next to the SQL; the page number, its size and
the client's sort arrive per call, apart from the filter arguments:

	query, err := page.Make(fetchWarehousesSQL,
		page.Head(page.Desc("is_active")),
		page.Tie(page.Asc("id")),
		page.SortKeyTag("json"),
	)

	warehouses, total, err := db.FetchPage[Warehouse](ctx, query,
		page.Request{Number: 2, Size: 20, Sort: []string{"cityEng:desc"}},
		params.Country,
	)

The sortable fields are derived from the model, so they cannot drift away from
the columns that are there; an unknown one is an error rather than a silent
skip. [page.Tie] is required, because a page without a total order repeats rows
on one page and loses them from another. The details - narrowing the whitelist,
counting over a cheaper statement, NULL placement - are in the
[github.com/uchaloop/pgfx/page] documentation.

The second statement is skipped whenever the rows already imply the total: a
page shorter than it asked for is the last one. When it does run, it is reported
to the metrics under the caller's query name with a ".count" suffix - it is a
different statement with a cost of its own, and it does not run every time.

An empty page is a valid result, not an error. [Tx.FetchPage] runs both
statements inside a transaction, where a repeatable read isolation level makes
the total agree with the page it describes.

# Transactions

[DB.Transaction] is the typed transaction entry point. It commits when its
callback returns nil and rolls back on an error or panic. The callback receives
a [Tx] with the same Fetch methods as DB and the embedded native pgx.Tx API:

	err := db.Transaction(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) error {
		order, err := tx.FetchRow[Order](ctx, selectOrder, id)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, updateOrder, order.ID)
		return err
	})

Inside the callback, use tx rather than db. A call through db uses the pool and
does not participate in the transaction.

The embedded pool's Begin and BeginTx remain the native manual API and return
pgx.Tx:

	tx, err := db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, insertOrder); err != nil {
		return err
	}
	return tx.Commit(ctx)

[Tx.Transaction] creates a typed savepoint:

	err := db.Transaction(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) error {
		return tx.Transaction(ctx, func(nested *pgfx.Tx) error {
			_, err := nested.Exec(ctx, insertOptionalData)
			return err
		})
	})

DB embeds *pgxpool.Pool, so COPY, LISTEN, acquired connections and the rest of
the native pool API are available directly. The pool itself is the DB.Pool
field; no accessor is needed.

# TLS

TLS_MODE follows libpq, and verify-full with TLS_ROOT_CERT is the setting that
actually verifies the server. [WithTLS] takes an in-memory *tls.Config instead,
for an application that already assembled one - from a secret store, or from a
certificate that never touched the filesystem.

# Tracing and metrics

Under Fx, [Module] enables OpenTelemetry query spans by itself when a
trace.TracerProvider is in the graph. [WithTracing] configures otelpgx directly,
and [WithTracer] installs any pgx.QueryTracer.

Query duration is reported through a callback rather than to a metrics library,
so pgfx holds no opinion about where it goes:

	fx.Supply(pgfx.QueryMetricFunc(func(metric pgfx.QueryMetric) {
		// metric.Name, metric.Duration, metric.Err
	}))

[WithQueryName] attaches a stable name to a query through the context, because
the SQL itself is not a metric label - it varies, and it is unbounded.

	ctx = pgfx.WithQueryName(ctx, "orders.get_by_id")

The SQL is left out of the metric by default. [WithSQLInQueryMetrics] includes
it, for a system where a query text is not something to hand to a metrics
pipeline by accident.
*/
package pgfx
