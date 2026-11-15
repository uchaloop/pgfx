package page

import (
	"fmt"
	"reflect"
	"strings"
)

// alias names the subquery a page is wrapped around.
const alias = "pgfx_page"

// Cols is the sort whitelist: the field name a client sends, mapped to the
// output column it orders by. Lookups are case-insensitive.
type Cols map[string]string

// Query is a SELECT together with the sort policy pagination may apply to it.
//
// The SELECT is written plain - the columns, the joins and the filter, nothing
// else. It carries no ORDER BY, no LIMIT, no row numbers and no count: a page
// is applied from the outside, around the query, which is what keeps the query
// readable and its rows decodable by the `db:"..."` tag like any other. Its
// arguments start at $1; the bounds are appended after them.
//
// The zero value is not usable; MakeQuery and MakeQueryOrPanic build one.
type Query struct {
	sql      string
	head     []Order
	tie      []Order
	sortable Cols
	tagKey   string
}

// Option configures a Query.
type Option func(*Query)

// WithHead fixes an order applied before the client's own: a pinned "active first",
// say. It is never overridden by a request.
func WithHead(orders ...Order) Option {
	return func(q *Query) {
		q.head = append(q.head, orders...)
	}
}

// WithTie sets the stabilizer applied after every other order, and is required: a
// page needs a total order, and only a unique column - the primary key, as a
// rule - provides one. Without it, rows with equal sort keys repeat on one page
// and vanish from another.
//
// The tie's own direction applies when it is the whole order, so WithTie(MakeDescOrder("id"))
// lists the newest rows first by default. After other orders it takes the
// direction of the last one: rows with equal keys may come in either order, and
// a single composite index, such as (updated_at, id), then serves the whole
// order in one scan.
func WithTie(orders ...Order) Option {
	return func(q *Query) {
		q.tie = append(q.tie, orders...)
	}
}

// WithSortable is the whitelist of fields a client may sort by, mapped to the
// output columns they order by. Expose sorts supported by the workload: without
// a suitable index, PostgreSQL may need to scan and sort many rows. Without WithSortable or
// WithSortKeyTag, a client may not sort at all.
func WithSortable(cols Cols) Option {
	return func(q *Query) {
		q.sortable = cols
	}
}

// WithSortKeyTag derives sortable names from the selected struct tag, such as json,
// and SQL columns from explicit db tags. Fields omitted by the selected tag or
// marked "-" are excluded. An exposed field without a db tag returns
// [ErrMissingColumnTag]. With tag "db", every non-ignored field needs a db tag.
// Anonymous struct values are flattened like pgx, ignoring tags on the embedding.
// Embedded pointers and named struct fields are not flattened.
// This option does not check indexes; use [WithSortable] to restrict expensive sorts.
// It is ignored when [WithSortable] is supplied.
func WithSortKeyTag(tag string) Option {
	return func(q *Query) {
		q.tagKey = tag
	}
}

// MakeQuery builds a reusable query and validates its static SQL and sort options.
// BuildStatement validates request values and any model-derived sorting.
// SQL syntax is checked by PostgreSQL.
func MakeQuery(sql string, opts ...Option) (Query, error) {
	q := Query{sql: trimSQL(sql)}

	for _, opt := range opts {
		if opt != nil {
			opt(&q)
		}
	}

	if len(q.sql) == 0 {
		return Query{}, ErrNoSQL
	}

	if len(q.tie) == 0 {
		return Query{}, ErrNoTie
	}

	for _, orders := range [][]Order{q.head, q.tie} {
		for _, order := range orders {
			if err := order.validate(); err != nil {
				return Query{}, err
			}
		}
	}

	if q.sortable != nil {
		sortable := make(Cols, len(q.sortable))

		for field, column := range q.sortable {
			if len(field) == 0 {
				return Query{}, fmt.Errorf("%w: %q", ErrUnknownSortField, field)
			}

			if err := (Order{Column: column}).validate(); err != nil {
				return Query{}, err
			}

			key := strings.ToLower(field)
			if previous, isOK := sortable[key]; isOK && previous != column {
				return Query{}, fmt.Errorf("%w: %q", ErrAmbiguousSortField, field)
			}

			sortable[key] = column
		}

		q.sortable = sortable
	}

	return q, nil
}

// MakeQueryOrPanic is MakeQuery for a query built once at package level, where a failure is a
// programming error and there is nobody to hand it to.
func MakeQueryOrPanic(sql string, opts ...Option) Query {
	q, err := MakeQuery(sql, opts...)
	if err != nil {
		panic(err)
	}

	return q
}

// Statement describes a page SELECT and a count of the same base query.
type Statement struct {
	// Rows takes the query's own arguments followed by PageArgs.
	Rows string

	// Count takes only the original query arguments and counts all base rows.
	Count string

	// PageArgs follow the original query arguments: Limit and Offset.
	PageArgs []any

	// Limit is the requested page size.
	Limit uint
}

// BuildStatement renders an OFFSET/LIMIT page and COUNT over the same base SELECT.
//
// model is the struct type used to decode rows. It is inspected only when
// req.Sort is nonempty and WithSortKeyTag supplies the whitelist (without WithSortable).
// In that case model must identify a struct; otherwise it may be nil.
// argc is the number of original filter arguments, whose placeholders start
// at $1. The generated LIMIT and OFFSET use $argc+1 and $argc+2, respectively.
// Execute Rows with the original arguments followed by PageArgs, and Count
// with only the original arguments. Wrapping the base SELECT preserves DISTINCT,
// GROUP BY and join multiplicity when counting.
func (q Query) BuildStatement(model reflect.Type, req Request, argc int) (Statement, error) {
	if len(q.sql) == 0 {
		return Statement{}, ErrNoSQL
	}

	limit, offset, err := req.bounds()
	if err != nil {
		return Statement{}, err
	}

	orders, err := q.orders(model, req.Sort)
	if err != nil {
		return Statement{}, err
	}

	return Statement{
		Rows: fmt.Sprintf(
			"SELECT * FROM (%s) AS %s ORDER BY %s LIMIT $%d OFFSET $%d",
			q.sql,
			alias,
			renderOrders(orders),
			argc+1,
			argc+2,
		),
		Count:    fmt.Sprintf("SELECT COUNT(*) FROM (%s) AS %s", q.sql, alias),
		PageArgs: []any{limit, offset},
		Limit:    limit,
	}, nil
}

// trimSQL strips the trailing semicolon and whitespace: the query becomes a
// subquery, and a statement terminator inside one is a syntax error.
func trimSQL(sql string) string {
	sql = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))

	// Preserve a line boundary before the wrapper's closing parenthesis. Without
	// it, a trailing SQL line comment consumes the rest of the generated query.
	// A newline is harmless when the marker occurs inside a quoted value.
	if strings.Contains(sql, "--") {
		sql += "\n"
	}

	return sql
}
