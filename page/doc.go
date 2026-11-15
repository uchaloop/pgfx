/*
Package page is the vocabulary of a paginated query: what a client asked for,
and what a query is allowed to order by.

It depends on nothing but the standard library, so the layers that speak about
pages - a handler that parsed the request, a domain interface that declares the
repository - can say so without importing a database driver. The execution
lives in pgfx: pgfx.DB.FetchPage and pgfx.Tx.FetchPage take a [Query] and a request.

# The query

A page is applied around a query, not woven into it. The SELECT stays plain -
columns, joins, filter - and pgfx wraps it:

	SELECT * FROM ( <query> ) AS pgfx_page ORDER BY ... LIMIT $n+1 OFFSET $n+2
	SELECT COUNT(*) FROM ( <query> ) AS pgfx_page

which is what keeps the filter written once, the arguments numbered from $1, and
the rows decodable by the `db:"..."` tag like every other row pgfx returns.

The order sees output columns only, so ordering by an expression means
selecting it under an alias.

	query, err := page.MakeQuery(
		fetchWarehousesSQL,
		page.WithHead(page.MakeDescOrder("is_active")),
		page.WithTie(page.MakeAscOrder("id")),
		page.WithSortable(page.Cols{"cityEng": "city_eng", "createdAt": "created_at"}),
	)

[WithTie] is required. A page needs a total order: without a unique column at the
end of it, rows with equal sort keys change places between two queries, and then
they repeat on one page and go missing from another. The tie keeps its own
direction when it is the whole order; after other orders it takes the direction
of the last one. A matching composite index can support this order; actual
plans also depend on predicates, NULL placement and PostgreSQL planner choices.

# Pagination

[Request] selects numbered LIMIT/OFFSET pages using Page and Limit, with
OFFSET = (Page - 1) * Limit. [Query.BuildStatement] renders both statements.
FetchPage executes the row query, then the count query with the original filter
arguments, and returns [Result] with List and Total. The row query reads at most
Limit rows. Total counts the complete base result, preserving DISTINCT, grouping
and joins, even when the requested page is empty or beyond the last page.

FetchPage does not create a transaction automatically. The queries can observe
different snapshots under concurrent writes. Use pgfx.Tx.FetchPage in an explicit
REPEATABLE READ transaction when the list and count must share a snapshot.

# The request

A [Request] is a page number, a page size and the sort fields as the client
spelled them:

	page.Request{Page: 2, Limit: 20, Sort: []string{"cityEng:desc"}}

Sort fields are matched case-insensitively against a whitelist, and an unknown
one is [ErrUnknownSortField] rather than a silent skip - a client that asked for
an order it did not get should hear about it. [WithSortable] is the whitelist: list
fields suitable for the workload; unsupported sorts may require scanning and
sorting many rows. [WithSortKeyTag] derives the whitelist from a struct tag such as
json. WithSortable
fields require explicit db tags; missing tags return [ErrMissingColumnTag].
Ignored fields are omitted, while anonymous struct values are flattened like
pgx, ignoring tags on the embedding itself. Without either option, a client
may not sort.

A zero Page or Limit returns [ErrInvalidRequest]. Substituting defaults for a page a
client did not fully specify belongs at the edge that parsed the request: only
that layer can tell "not sent" from "sent as zero", and the default is part of
the API contract rather than of the query.

# NULLs

[Order] leaves NULL placement to Postgres by default - NULLS LAST ascending,
NULLS FIRST descending. That is what a plain btree index yields in either scan
direction. A different NULL placement may require a different index or an
explicit sort. [Nulls] selects the placement for each order term. NULL placement
does not affect the correctness of a page: the tie order is what keeps it
stable.
*/
package page
