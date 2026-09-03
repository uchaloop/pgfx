package page

import (
	"fmt"
	"strings"
)

// Nulls is where NULLs go within one ORDER BY term.
//
// The zero value keeps the Postgres default - NULLS LAST ascending, NULLS FIRST
// descending - which is exactly what a plain btree index yields in either scan
// direction. Overriding it is a valid choice, but it stops matching a default
// index and turns an index scan into a sort of the whole filtered set, so it
// belongs to a query that has an index built for it rather than to a
// library-wide policy.
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
	if len(o.Column) == 0 || strings.ContainsRune(o.Column, 0) {
		return fmt.Errorf("%w: %q", ErrInvalidColumn, o.Column)
	}

	return nil
}

// sql renders the term with its column quoted.
func (o Order) sql() string {
	var b strings.Builder

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
	case NullsDefault:
	}

	return b.String()
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
