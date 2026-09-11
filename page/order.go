package page

import (
	"fmt"
	"reflect"
	"strings"
)

// Nulls is where NULLs go within one ORDER BY term.
//
// The zero value keeps the Postgres default - NULLS LAST ascending, NULLS FIRST
// descending - which is exactly what a plain btree index yields in either scan
// direction. Overriding it may require a different index or an explicit sort.
// Choose placement for the query rather than as a library-wide policy.
type Nulls uint8

const (
	// NullsDefault leaves NULL placement to Postgres.
	NullsDefault Nulls = iota
	// NullsFirst puts NULLs before every other value.
	NullsFirst
	// NullsLast puts NULLs after every other value.
	NullsLast
)

// Order is one ORDER BY term over an output column of a paginated query.
//
// Column is an output column, not an expression: the order is applied to the
// query from the outside, so only its result columns are in scope. To order by
// an expression, select it under an alias. The column is quoted, never
// interpolated as SQL.
type Order struct {
	Column string
	Desc   bool
	Nulls  Nulls
}

// Asc orders by column ascending, leaving NULL placement to Postgres.
func Asc(column string) Order {
	return Order{Column: column}
}

// Desc orders by column descending, leaving NULL placement to Postgres.
func Desc(column string) Order {
	return Order{Column: column, Desc: true}
}

// validate reports whether the term can be rendered as an identifier.
func (o Order) validate() error {
	if o.Nulls > NullsLast {
		return ErrInvalidNulls
	}
	if len(o.Column) == 0 || strings.ContainsRune(o.Column, 0) {
		return fmt.Errorf("%w: %q", ErrInvalidColumn, o.Column)
	}

	return nil
}

// writeSQL appends one validated order term.
func (o Order) writeSQL(b *strings.Builder) {
	b.WriteString(quoteIdent(o.Column))
	if o.Desc {
		b.WriteString(" DESC")
	} else {
		b.WriteString(" ASC")
	}
	switch o.Nulls {
	case NullsFirst:
		b.WriteString(" NULLS FIRST")
	case NullsLast:
		b.WriteString(" NULLS LAST")
	}
}

// quoteIdent renders name as a quoted SQL identifier, so that a whitelisted
// column cannot be read as anything but a column.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// parseSort splits one client sort value: "field" or "field:desc".
func parseSort(value string) (field string, desc bool, err error) {
	field, direction, _ := strings.Cut(strings.TrimSpace(value), ":")

	field = strings.TrimSpace(field)
	if len(field) == 0 {
		return "", false, fmt.Errorf("%w: %q", ErrInvalidSort, value)
	}

	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "", "asc":
		return field, false, nil
	case "desc":
		return field, true, nil
	default:
		return "", false, fmt.Errorf("%w: %q", ErrInvalidSort, value)
	}
}

func (q Query) orders(model reflect.Type, sort []string) ([]Order, error) {
	sortable := q.sortable
	if sortable == nil && len(sort) > 0 {
		var err error
		sortable, err = sortableOf(model, q.tagKey)
		if err != nil {
			return nil, err
		}
	}
	return q.resolveOrders(sortable, sort)
}

func renderOrders(orders []Order) string {
	var sql strings.Builder
	for i, order := range orders {
		if i > 0 {
			sql.WriteString(", ")
		}
		order.writeSQL(&sql)
	}
	return sql.String()
}

// resolveOrders assembles the ORDER BY: the head, then what the client asked for,
// then the tie. A column already ordered by is not repeated, so a request that
// names the tie column keeps the direction it asked for.
func (q Query) resolveOrders(sortable Cols, sort []string) ([]Order, error) {
	parts := make([]Order, 0, len(q.head)+len(sort)+len(q.tie))
	seen := make(map[string]struct{}, cap(parts))

	add := func(order Order) {
		if _, ok := seen[order.Column]; ok {
			return
		}

		seen[order.Column] = struct{}{}
		parts = append(parts, order)
	}

	for _, order := range q.head {
		add(order)
	}

	for _, value := range sort {
		field, desc, err := parseSort(value)
		if err != nil {
			return nil, err
		}

		column, ok := sortable[strings.ToLower(field)]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownSortField, field)
		}

		add(Order{Column: column, Desc: desc})
	}

	for _, order := range q.tie {
		add(order)
	}

	return parts, nil
}
