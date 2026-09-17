<p align="center">
  <img src="logo.png" alt="pgfx" width="320">
</p>

<p align="center">
  <a href="https://github.com/uchaloop/pgfx/actions/workflows/ci.yml"><img src="https://github.com/uchaloop/pgfx/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/uchaloop/pgfx"><img src="https://pkg.go.dev/badge/github.com/uchaloop/pgfx.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/uchaloop/pgfx" alt="License: MIT"></a>
</p>

A thin client extending [pgx](https://github.com/jackc/pgx) with typed fetches,
pagination, transaction callbacks and optional Fx integration and observability.
SQL stays explicit; the native pgx API remains available through the embedded
pool and transaction. pgfx is not an ORM or a replacement driver.

- **Config is data, runtime is options** - what survives a round trip through
  text is `Config`; a tracer, a metrics callback, an in-memory `*tls.Config` are
  `Option` values. Nothing is configured twice.
- **It reads no config source** - the application supplies the `Config`, so pgfx
  is tied to no particular loader.
- **Fx verifies connectivity on start**; with `Make`, call `Ping` explicitly
  when startup verification is needed.
- **No second query API** - `DB` and `Tx` add typed fetches while preserving pgx
  for execution, transactions and advanced operations.

```bash
go get github.com/uchaloop/pgfx
```

## Quick start

Build a `pgfx.Config` in Go and supply it to Fx. The application chooses where
configuration values come from; pgfx does not require a configuration loader.

```go
import (
    "os"
    "time"

    "github.com/uchaloop/pgfx"
    "github.com/uchaloop/secret/v2"
    "go.uber.org/fx"
)

cfg := pgfx.Config{
    Host:     "localhost:5432",
    Database: "orders",
    User:     "orders",
    Password: secret.New(os.Getenv("POSTGRES_PASSWORD")),
    Pool:     pgfx.PoolConfig{MaxConns: 20},
    Timeouts: pgfx.TimeoutConfig{Connect: 5 * time.Second},
}

fx.New(
    fx.Supply(cfg),
    pgfx.Module, // untagged *pgfx.DB, verified at start, closed at stop
).Run()
```

For a replica, supply another config with an Fx name and use the matching module:

```go
replicaCfg := cfg
replicaCfg.Host = "replica:5432"

fx.New(
    fx.Supply(cfg),
    pgfx.Module,
    fx.Supply(fx.Annotate(replicaCfg, fx.ResultTags(`name:"replica"`))),
    pgfx.ModuleFor("replica"),
).Run()
```

Without Fx, pass the same config directly and close the connection when done:

```go
db, err := pgfx.Make(ctx, cfg)
if err != nil {
    return err
}
defer db.Close()
```

Fx provides only `*pgfx.DB`; the embedded pool is available as `db.Pool` and is
not registered separately.

### Optional: confmaker

For convenient environment loading and Fx integration, we recommend
[confmaker](https://github.com/uchaloop/confmaker). Its `confx` package can replace
manual config construction and `fx.Supply(cfg)`:

```go
// import "github.com/uchaloop/confmaker/confx"
fx.New(
    confx.Module(),
    confx.Provide[pgfx.Config](), // POSTGRES_HOST, POSTGRES_DATABASE, ...
    pgfx.Module,
).Run()
```

`Config` names its default instance `postgres`, which gives the `POSTGRES_`
prefix. For a named replica, add these options to the same application:

```go
confx.ProvideNamed[pgfx.Config]("replica"), // REPLICA_HOST, REPLICA_DATABASE, ...
pgfx.ModuleFor("replica"),
```

## Queries

```go
order, err := db.FetchRow[Order](
	ctx,
	`SELECT id, amount FROM orders WHERE id = $1`,
	id,
)

total, err := db.FetchValue[int64](ctx, `SELECT count(*) FROM orders`)
```

`FetchRows` and `FetchRow` decode struct rows by the `db:"..."` tag;
`FetchValues` and `FetchValue` decode a single column into a scalar. The type to
decode into is an explicit type argument, because nothing in the arguments
implies it.

## Pagination

`FetchPage` returns one page and the number of rows the filter matches. The
query stays a plain `SELECT` - no `ORDER BY`, no `LIMIT`, no row numbers, no
count - and the page is wrapped around it, so the filter is written once and the
rows decode by the `db` tag like any other:

```go
var warehouses = page.Must(
	fetchWarehousesSQL,
	page.Head(page.Desc("is_active")), // pinned, before the client's sort
	page.Tie(page.Asc("id")),          // required: a page needs a total order
	page.Sortable(page.Cols{           // what a client may sort by
		"cityEng":   "city_eng",
		"createdAt": "created_at",
	}),
)

rows, total, err := db.FetchPage[Warehouse](
	ctx,
	warehouses,
	page.Request{Number: 2, Size: 20, Sort: []string{"cityEng:desc"}},
	params.Country,
)
```

A client sorts only by what `Sortable` lists, and an unknown field is an error
rather than a silent skip. List the fields an index serves: a sort without one
reads and sorts the whole result on every page. `page.SortKeyTag("json")` opens
every field of the model instead, under its json name - convenient for small
tables, costly for large ones.

The tie keeps its own direction when it is the whole order, so `Tie(Desc("id"))`
lists the newest rows first by default. After other orders it takes the
direction of the last one: `cityEng:desc` gives `ORDER BY city_eng DESC, id DESC`,
which one index on `(city_eng, id)` serves in a single backward scan.

The rows statement reads one row past the page. The total takes a second
statement, skipped when a nonempty page has no row after it: that page is the
last one, and the total follows from it.

Page number and size arrive as a `page.Request`, apart from the filter: a
default for a page a client did not fully specify belongs at the edge that
parsed the request, and a zero here is a bug rather than a request for page one.

Numbered pages use `LIMIT/OFFSET`. They work well for shallow pages and direct
page-number navigation. Large offsets require PostgreSQL to walk skipped rows;
for sequential traversal, use the cursor API below. Performance depends on the
filtered result and matching indexes, not just the table's row count.

### Pages without a total

Counting is often the most expensive part of a page. When a client needs only
the rows and a "next" link, use `FetchPageRows` with the same query and request.
It executes only the page SELECT and tells from the extra row whether another
page follows:

```go
list, more, err := db.FetchPageRows[Warehouse](
    ctx,
    warehouses,
    page.Request{Number: 2, Size: 20},
    params.Country,
)
```

Inside a transaction use `tx.FetchPageRows`. No count SQL is built or executed.

### Cursor pages

```go
result, err := db.FetchAfter[Warehouse](
    ctx,
    warehouses,
    page.CursorRequest{
        Size: 20,
        After: previousCursor,
        Sort: []string{"cityEng:desc"},
    },
    params.Country,
)
// result.List, result.NextCursor, result.HasMore
```

Start with an empty `After`, then pass the returned `NextCursor` while `HasMore`
is true. The size can change; SQL, filters and effective sorting must remain the
same. There is no automatic switch between offset and cursor: they provide
different navigation contracts. `CursorResult` carries no JSON tags - the shape
of the response belongs to the API.

Cursor queries fetch at most `Size + 1` result rows and never count. Uniform
sort directions use a tuple comparison; mixed directions and NULL transitions
use disjoint, individually limited `UNION ALL` branches. Matching indexes are
essential. Expensive joins, expression sorting and unindexed filters can still
make a cursor query expensive. Every sort column must be selected, and the tie
columns must uniquely order the result (including rows multiplied by joins).

When needed, request the exact total separately:

```go
total, err := db.FetchTotal(ctx, warehouses, params.Country)
```

`FetchTotal` counts all rows of the base filter, without cursor bounds, ordering
or offset. `page.CountSQL` can supply an equivalent cheaper SELECT. Exact totals
can dominate query time on large results; cursor seeking does not make counting
cheap. Both methods are available on `Tx`.

Struct decoding still honors `db` tags and pgx JSON/JSONB map codecs. Cursor keys
support integer and finite floating-point scalars, bool, string, `time.Time`,
`[]byte`, UUID as `[16]byte`, and SQL NULL. These are pgx-decoded key values;
`numeric`, arbitrary custom pgx types and maps are not supported as cursor keys.
Filter arguments must have stable JSON representations. Tokens are versioned
and bound to the query, but are neither signed nor encrypted; enforce access
filters independently. Concurrent updates to sort keys can move rows between
pages. A consistent snapshot remains an explicit transaction choice.

A key range such as `WHERE id BETWEEN $1 AND $2` is ordinary SQL for
`FetchRows`, not a pagination mode. pgfx is a thin pgx extension and does not
integrate search-engine pagination: when Elasticsearch/OpenSearch supplies
ranked results, paginate there and use PostgreSQL to fetch the selected IDs,
preserving the search result order.

## Transactions

`Transaction` commits on nil and rolls back on an error or panic. Its `Tx` has
the same typed fetches as `DB` and embeds the native `pgx.Tx` API:

```go
err := db.Transaction(
    ctx,
    pgx.TxOptions{},
    func(tx *pgfx.Tx) error {
        order, err := tx.FetchRow[Order](ctx, selectOrder, id)
        if err != nil {
            return err
        }

        _, err = tx.FetchValue[int64](
            ctx,
            `UPDATE orders SET status = 'paid' WHERE id = $1 RETURNING id`,
            order.ID,
        )
        return err
    },
)
```

Use `tx`, not `db`, inside the callback: a call through `db` uses the pool and
does not participate in the transaction.

For manual ownership use the embedded native `db.Begin` or `db.BeginTx`; they
return `pgx.Tx`. `tx.BeginNested` and `tx.Transaction` create typed pgx
savepoints. The embedded pool also makes COPY, LISTEN, acquired connections and
the rest of the native API available directly on `DB`; the pool itself is
`db.Pool`.

Pagination does not open a transaction automatically. To keep list and total
in one snapshot under concurrent writes, explicitly use `pgx.RepeatableRead`
with `tx.FetchPage`.

## Environment configuration with confmaker

With the optional confmaker setup above, a deployment can supply:

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

Host and database are required. Unset optional fields retain their documented
defaults. `confmaker.Manifest[pgfx.Config]()` lists the whole set with types
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
