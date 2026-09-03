package page

import "errors"

var (
	// ErrNoSQL is reported by Make when the query text is empty.
	ErrNoSQL = errors.New("empty page query")

	// ErrNoTie is reported by Make when no tie order is given. A page needs a
	// total order: without a stabilizer, rows with equal sort keys are free to
	// change places between two pages, and then they repeat on one page and go
	// missing from another.
	ErrNoTie = errors.New("page query has no tie order")

	// ErrInvalidColumn is reported when an order or a whitelist names a column
	// that cannot be rendered as an identifier.
	ErrInvalidColumn = errors.New("invalid order column")

	// ErrInvalidRequest is reported when a page number or size is zero.
	// Substituting defaults for them belongs at the edge that parsed the
	// request; here a zero is a bug, not a request for the first page.
	ErrInvalidRequest = errors.New("page number and size must be greater than zero")

	// ErrInvalidSort is reported when a sort value is malformed - an empty field
	// or a direction other than asc and desc.
	ErrInvalidSort = errors.New("invalid sort value")

	// ErrUnknownSortField is reported when a sort field is not in the whitelist.
	// It is an error rather than a silent skip: a client that asked for an order
	// it did not get should hear about it.
	ErrUnknownSortField = errors.New("unknown sort field")

	// ErrUnsortableModel is reported when the sort whitelist has to be derived
	// from a model that is not a struct. Give the whitelist with Sortable, or
	// decode into a struct.
	ErrUnsortableModel = errors.New("sort whitelist needs a struct model")
)
