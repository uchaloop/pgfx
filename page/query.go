package page

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// alias names the subquery a page is wrapped around.
const alias = "pgfx_page"

const maxCursorKeys = 32

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
//
// The tie's own direction applies when it is the whole order, so Tie(Desc("id"))
// lists the newest rows first by default. After other orders it takes the
// direction of the last one: rows with equal keys may come in either order, and
// a single composite index, such as (updated_at, id), then serves the whole
// order in one scan.
func Tie(orders ...Order) Option {
	return func(q *Query) {
		q.tie = append(q.tie, orders...)
	}
}

// Sortable is the whitelist of fields a client may sort by, mapped to the
// output columns they order by. List only what an index serves: a sort without
// one reads and sorts the whole result on every page. Without Sortable or
// SortKeyTag, a client may not sort at all.
func Sortable(cols Cols) Option {
	return func(q *Query) {
		q.sortable = cols
	}
}

// SortKeyTag lets a client sort by any field of the model the rows decode into,
// named by the given struct tag: "db" for column names, "json" for the names of
// the API, with the db tag giving the column. Every column becomes sortable,
// including ones without an index, so prefer Sortable for large results. It is
// ignored when Sortable is given.
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
			if previous, ok := sortable[key]; ok && previous != column {
				return Query{}, fmt.Errorf("%w: %q", ErrAmbiguousSortField, field)
			}
			sortable[key] = column
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
	// Rows takes the query's own arguments followed by PagingArgs. It reads one
	// row past the page: that row only tells whether another page follows.
	Rows string

	// Count takes the query's own arguments alone. It is not always run: a
	// nonempty page with no row after it already says how many rows there are.
	Count string

	// PagingArgs follow the original query arguments: Limit+1 and Offset.
	PagingArgs []any

	// Limit and Offset describe page size and skipped positions.
	Limit  uint
	Offset uint
}

// Build renders both statements for one request.
//
// The model is the struct the rows decode into, and is only read when the
// whitelist has to be derived - that is, when a sort was actually requested and
// SortKeyTag is set. argc is how many arguments the query itself takes.
func (q Query) Build(model reflect.Type, req Request, argc int) (Statements, error) {
	statements, err := q.BuildRows(model, req, argc)
	if err != nil {
		return Statements{}, err
	}

	statements.Count, err = q.BuildCount()

	return statements, err
}

// BuildRows renders only the OFFSET/LIMIT page; Count is left empty.
func (q Query) BuildRows(model reflect.Type, req Request, argc int) (Statements, error) {
	if len(q.sql) == 0 {
		return Statements{}, ErrNoSQL
	}

	limit, offset, err := req.bounds()
	if err != nil {
		return Statements{}, err
	}

	orders, err := q.orders(model, req.Sort)
	if err != nil {
		return Statements{}, err
	}

	return Statements{
		Rows:       fmt.Sprintf("SELECT * FROM (%s) AS %s ORDER BY %s LIMIT $%d OFFSET $%d", q.sql, alias, renderOrders(orders), argc+1, argc+2),
		PagingArgs: []any{limit + 1, offset},
		Limit:      limit,
		Offset:     offset,
	}, nil
}

// BuildCount wraps the base SELECT (or CountSQL override), without ordering or
// pagination. It accepts the original filter arguments only.
func (q Query) BuildCount() (string, error) {
	if len(q.sql) == 0 {
		return "", ErrNoSQL
	}

	counted := q.countSQL
	if len(counted) == 0 {
		counted = q.sql
	}

	return "SELECT count(*) FROM (" + counted + ") AS " + alias, nil
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

// CursorStatement is a prepared keyset query. Args includes filter arguments,
// cursor keys and the look-ahead limit. Orders names the result columns used
// to create the next cursor. Treat a prepared statement as immutable.
type CursorStatement struct {
	Rows   string
	Args   []any
	Orders []Order
	scope  string
}

// BuildAfter prepares a keyset page. Cursor keys are parameter values, never SQL.
// Same-direction non-NULL keys use a row comparison. NULL transitions and mixed
// directions use disjoint, individually limited UNION ALL branches.
// Filter arguments must be JSON-serializable with stable representations.
func (q Query) BuildAfter(model reflect.Type, req CursorRequest, args ...any) (CursorStatement, error) {
	if len(q.sql) == 0 {
		return CursorStatement{}, ErrNoSQL
	}

	if req.Size == 0 || uint64(req.Size) >= uint64(math.MaxInt64) {
		return CursorStatement{}, ErrInvalidRequest
	}

	orders, err := q.orders(model, req.Sort)
	if err != nil {
		return CursorStatement{}, err
	}

	if len(orders) > maxCursorKeys {
		return CursorStatement{}, ErrInvalidRequest
	}

	scope, err := cursorScope(q.sql, orders, args)
	if err != nil {
		return CursorStatement{}, err
	}

	var values []any
	if req.After != "" {
		values, err = decodeCursor(req.After, scope, len(orders))
		if err != nil {
			return CursorStatement{}, err
		}
	}

	keys, params := prepareCursorKeys(orders, values, args)
	params = append(params, req.Size+1)

	return CursorStatement{
		Rows:   q.afterSQL(orders, keys, len(params)),
		Args:   params,
		Orders: orders,
		scope:  scope,
	}, nil
}

// cursorKey keeps one SQL comparison together instead of relying on matching
// indices in separate order, value and placeholder slices.
type cursorKey struct {
	order     Order
	column    string
	parameter string // Empty for SQL NULL, which needs no bound parameter.
}

func prepareCursorKeys(orders []Order, values, args []any) ([]cursorKey, []any) {
	params := make([]any, len(args), len(args)+len(values)+1)
	copy(params, args)
	keys := make([]cursorKey, len(values))
	for i, value := range values {
		keys[i] = cursorKey{order: orders[i], column: quoteIdent(orders[i].Column)}
		if value != nil {
			params = append(params, value)
			keys[i].parameter = fmt.Sprintf("$%d", len(params))
		}
	}
	return keys, params
}

func (q Query) afterSQL(orders []Order, keys []cursorKey, limitPosition int) string {
	limit := fmt.Sprintf("$%d", limitPosition)
	suffix := " ORDER BY " + renderOrders(orders) + " LIMIT " + limit
	selectSQL := "SELECT * FROM (" + q.sql + ") AS " + alias
	if len(keys) == 0 {
		return selectSQL + suffix
	}
	predicates := afterPredicates(keys)
	switch len(predicates) {
	case 0:
		return selectSQL + " WHERE FALSE" + suffix
	case 1:
		return selectSQL + " WHERE " + predicates[0] + suffix
	}
	var sql strings.Builder
	size := len("SELECT * FROM () AS pgfx_after") + len(suffix) + (len(predicates)-1)*len(" UNION ALL ")
	for _, predicate := range predicates {
		size += len(selectSQL) + len(" WHERE ") + len(predicate) + len(suffix) + 2
	}
	sql.Grow(size)
	sql.WriteString("SELECT * FROM (")
	for i, predicate := range predicates {
		if i > 0 {
			sql.WriteString(" UNION ALL ")
		}
		sql.WriteByte('(')
		sql.WriteString(selectSQL)
		sql.WriteString(" WHERE ")
		sql.WriteString(predicate)
		sql.WriteString(suffix)
		sql.WriteByte(')')
	}
	sql.WriteString(") AS pgfx_after")
	sql.WriteString(suffix)
	return sql.String()
}

func nullsFirst(order Order) bool {
	return order.Nulls == NullsFirst || (order.Nulls == NullsDefault && order.Desc)
}

func afterPredicates(keys []cursorKey) []string {
	tuple := true
	for _, key := range keys {
		if key.order.Desc != keys[0].order.Desc || key.parameter == "" {
			tuple = false
			break
		}
	}
	predicates := make([]string, 0, len(keys)+1)
	// Tuple comparison seeks uniform non-NULL keys. Separate disjoint branches
	// handle NULL-last transitions omitted by SQL's UNKNOWN comparison.
	if tuple {
		predicates = append(predicates, tuplePredicate(keys))
	}
	prefix := ""
	for _, key := range keys {
		if key.parameter == "" {
			if nullsFirst(key.order) {
				predicates = append(predicates, prefix+key.column+" IS NOT NULL")
			}
			prefix += key.column + " IS NULL AND "
			continue
		}
		if !tuple {
			predicates = append(predicates, prefix+key.column+comparison(key.order)+key.parameter)
		}
		if !nullsFirst(key.order) {
			predicates = append(predicates, prefix+key.column+" IS NULL")
		}
		prefix += key.column + " = " + key.parameter + " AND "
	}
	return predicates
}

func comparison(order Order) string {
	if order.Desc {
		return " < "
	}
	return " > "
}

func tuplePredicate(keys []cursorKey) string {
	if len(keys) == 1 {
		return keys[0].column + comparison(keys[0].order) + keys[0].parameter
	}
	var sql strings.Builder
	sql.WriteByte('(')
	for i, key := range keys {
		if i > 0 {
			sql.WriteString(", ")
		}
		sql.WriteString(key.column)
	}
	sql.WriteByte(')')
	sql.WriteString(comparison(keys[0].order))
	sql.WriteByte('(')
	for i, key := range keys {
		if i > 0 {
			sql.WriteString(", ")
		}
		sql.WriteString(key.parameter)
	}
	sql.WriteByte(')')
	return sql.String()
}
