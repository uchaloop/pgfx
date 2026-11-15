package page

import "math"

// Request is one page as a client asked for it: a 1-based page number, a page
// size, and the sort fields in the client's own vocabulary.
//
// Defaults for a page a client did not fully specify belong at the edge that
// parsed the request - it is the only layer that can tell "not sent" from "sent
// as zero", and the default is part of the API contract, not of the query. A
// Request that reaches the database is expected to be complete.
type Request struct {
	// Page is the 1-based page number.
	Page uint

	// Limit is how many rows the page holds. pgfx reserves Limit result elements.
	// Validate an application-specific maximum before fetching.
	Limit uint

	// Sort holds the requested order as "field" or "field:desc", in the names a
	// client uses rather than in column names. It may be empty: the query's own
	// head and tie orders still apply.
	Sort []string
}

// bounds converts the request into the LIMIT and OFFSET of the rows statement.
func (r Request) bounds() (limit, offset uint, err error) {
	// PostgreSQL takes LIMIT as a bigint.
	if r.Page == 0 || r.Limit == 0 || uint64(r.Limit) > uint64(math.MaxInt64) {
		return 0, 0, ErrInvalidRequest
	}

	// Postgres takes OFFSET as a bigint, and the product below wraps silently on
	// a page number no client has any business sending.
	if uint64(r.Page-1) > uint64(math.MaxInt64)/uint64(r.Limit) {
		return 0, 0, ErrInvalidRequest
	}

	return r.Limit, (r.Page - 1) * r.Limit, nil
}

// Result contains the selected page and the total number of base query rows.
// An empty successful page has a non-nil List and still includes Total.
// Response serialization belongs to the application.
type Result[T any] struct {
	List  []T
	Total int64
}
