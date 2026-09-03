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
// The zero value is not usable; Make and Must build one.
type Query struct {
	sql      string
	countSQL string
	head     []Order
	tie      []Order
	sortable Cols
	tagKey   string
}

// Option configures a Query.
type Option func(*Query)

// Head fixes an order applied before the client's own: a pinned "active first",
// say. It is never overridden by a request.
func Head(orders ...Order) Option {
	return func(q *Query) {
		q.head = append(q.head, orders...)
	}
}

// Tie sets the stabilizer applied after every other order, and is required: a
// page needs a total order, and only a unique column - the primary key, as a
// rule - provides one. Without it, rows with equal sort keys repeat on one page
// and vanish from another.
func Tie(orders ...Order) Option {
	return func(q *Query) {
		q.tie = append(q.tie, orders...)
	}
}

// Sortable replaces the derived whitelist with an explicit one, which is how a
// column that is expensive to order by is kept out of the client's reach.
func Sortable(cols Cols) Option {
	return func(q *Query) {
		q.sortable = cols
	}
}

// SortKeyTag names the struct tag holding the field name a client sorts by. It
// defaults to "db", where the sortable fields are exactly the model's columns.
// A model that carries both tags maps one vocabulary onto the other with
// SortKeyTag("json"): the json tag is what the client sends, the db tag is the
// column it orders by. It is ignored when Sortable is given.
func SortKeyTag(tag string) Option {
	return func(q *Query) {
		q.tagKey = tag
	}
}

// CountSQL replaces the query the total is counted over. The count runs the
// same filter a second time, so a query whose joins only add columns can be
// counted over a cheaper statement - as long as it matches the same rows and
// takes the same arguments.
func CountSQL(sql string) Option {
	return func(q *Query) {
		q.countSQL = trimSQL(sql)
	}
}

// Make builds a page query and validates it once, at startup rather than per
// request.
func Make(sql string, opts ...Option) (Query, error) {
	q := Query{sql: trimSQL(sql), tagKey: defaultTagKey}

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

			sortable[strings.ToLower(field)] = column
		}

		q.sortable = sortable
	}

	return q, nil
}

// Must is Make for a query built once at package level, where a failure is a
// programming error and there is nobody to hand it to.
func Must(sql string, opts ...Option) Query {
	q, err := Make(sql, opts...)
	if err != nil {
		panic(err)
	}

	return q
}

// Statements are the two statements one page takes.
type Statements struct {
	// Rows takes the query's own arguments followed by Limit and Offset.
	Rows string

	// Count takes the query's own arguments alone. It is not always run: a page
	// shorter than Limit already says how many rows there are.
	Count string

	Limit  uint
	Offset uint
}

// Build renders the statements for one request.
//
// The model is the struct the rows decode into, and is only read when the
// whitelist has to be derived - that is, when a sort was actually requested and
// Sortable was not given. argc is how many arguments the query itself takes.
func (q Query) Build(model reflect.Type, req Request, argc int) (Statements, error) {
	limit, offset, err := req.bounds()
	if err != nil {
		return Statements{}, err
	}

	sortable := q.sortable
	if sortable == nil && len(req.Sort) > 0 {
		if sortable, err = sortableOf(model, q.tagKey); err != nil {
			return Statements{}, err
		}
	}

	order, err := q.orderBy(sortable, req.Sort)
	if err != nil {
		return Statements{}, err
	}

	counted := q.countSQL
	if len(counted) == 0 {
		counted = q.sql
	}

	return Statements{
		Rows: fmt.Sprintf(
			"SELECT * FROM (%s) AS %s ORDER BY %s LIMIT $%d OFFSET $%d",
			q.sql, alias, order, argc+1, argc+2,
		),
		Count:  fmt.Sprintf("SELECT count(*) FROM (%s) AS %s", counted, alias),
		Limit:  limit,
		Offset: offset,
	}, nil
}

// orderBy assembles the ORDER BY: the head, then what the client asked for,
// then the tie. A column already ordered by is not repeated, so a request that
// names the tie column keeps the direction it asked for.
func (q Query) orderBy(sortable Cols, sort []string) (string, error) {
	parts := make([]string, 0, len(q.head)+len(sort)+len(q.tie))
	seen := make(map[string]struct{}, cap(parts))

	add := func(order Order) {
		if _, ok := seen[order.Column]; ok {
			return
		}

		seen[order.Column] = struct{}{}
		parts = append(parts, order.sql())
	}

	for _, order := range q.head {
		add(order)
	}

	for _, value := range sort {
		field, desc, err := parseSort(value)
		if err != nil {
			return "", err
		}

		column, ok := sortable[strings.ToLower(field)]
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrUnknownSortField, field)
		}

		add(Order{Column: column, Desc: desc})
	}

	for _, order := range q.tie {
		add(order)
	}

	return strings.Join(parts, ", "), nil
}

// trimSQL strips the trailing semicolon and whitespace: the query becomes a
// subquery, and a statement terminator inside one is a syntax error.
func trimSQL(sql string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))
}
