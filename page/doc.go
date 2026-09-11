/*
Package page is the vocabulary of a paginated query: what a client asked for,
and what a query is allowed to order by.

It depends on nothing but the standard library, so the layers that speak about
pages - a handler that parsed the request, a domain interface that declares the
repository - can say so without importing a database driver. The execution
lives in pgfx: pgfx.DB.FetchPage and pgfx.DB.FetchPageRows take a [Query] and a [Request].

# The query

A page is applied around a query, not woven into it. The SELECT stays plain -
columns, joins, filter - and pgfx wraps it:

	SELECT * FROM ( <query> ) AS pgfx_page ORDER BY ... LIMIT $n+1 OFFSET $n+2
	SELECT count(*) FROM ( <query> ) AS pgfx_page

which is what keeps the filter written once, the arguments numbered from $1, and
the rows decodable by the `db:"..."` tag like every other row pgfx returns.

Two consequences are worth knowing. The order sees output columns only, so
ordering by an expression means selecting it under an alias. And the count runs
the filter a second time; [CountSQL] gives it a cheaper statement when the joins
are there only for columns.

	query, err := page.Make(
		fetchWarehousesSQL,
		page.Head(page.Desc("is_active")),
		page.Tie(page.Asc("id")),
		page.SortKeyTag("json"),
	)

[Tie] is required. A page needs a total order: without a unique column at the
end of it, rows with equal sort keys change places between two queries, and then
they repeat on one page and go missing from another.

# Pagination strategies

[Request] selects numbered LIMIT/OFFSET pages. FetchPage counts the base SELECT
without the added order or page bounds. A nonempty incomplete page can infer
the total; full and empty pages require counting. FetchPageRows builds only the
rows statement through [Query.BuildRows]. [Query.BuildCount] builds a separate
count, while [Query.Build] remains available when both statements are needed.

[CursorRequest] selects keyset pages through pgfx.DB.FetchAfter or
pgfx.Tx.FetchAfter. [Query.BuildAfter] uses comparisons of sort keys, with
limited UNION ALL branches for mixed directions and NULL transitions. A matching
B-tree index is needed for efficient seeking; arbitrary joins, expressions and
sorts can still require substantial work. Cursor pagination does not provide
random page numbers. FetchTotal explicitly counts the complete base filter.
Neither pagination approach creates a transaction automatically.

BETWEEN is not a pagination mode. Custom key-range queries remain ordinary SQL
and can be executed with FetchRows.

# The request

A [Request] is a page number, a page size and the sort fields as the client
spelled them:

	page.Request{Number: 2, Size: 20, Sort: []string{"city_eng:desc"}}

Sort fields are matched case-insensitively against a whitelist, and an unknown
one is [ErrUnknownSortField] rather than a silent skip - a client that asked for
an order it did not get should hear about it. The whitelist is derived from the
model the rows decode into, so it cannot drift away from the columns that are
actually there; [Sortable] narrows it, and [SortKeyTag] switches the names a
client uses to another tag on the same model.

A zero number or size is [ErrInvalidRequest]. Substituting defaults for a page a
client did not fully specify belongs at the edge that parsed the request: only
that layer can tell "not sent" from "sent as zero", and the default is part of
the API contract rather than of the query.

# NULLs

[Order] leaves NULL placement to Postgres by default - NULLS LAST ascending,
NULLS FIRST descending. That is what a plain btree index yields in either scan
direction. A different NULL placement may require a different index or an
explicit sort. [Nulls] selects the placement for each order term. NULL placement does not affect the correctness of a page: the tie
order is what keeps it stable.
*/
package page
