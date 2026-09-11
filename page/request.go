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
	// Number is the 1-based page number.
	Number uint

	// Size is how many rows the page holds.
	Size uint

	// Sort holds the requested order as "field" or "field:desc", in the names a
	// client uses rather than in column names. It may be empty: the query's own
	// head and tie orders still apply.
	Sort []string
}

// bounds converts the request into the LIMIT and OFFSET of the rows statement.
func (r Request) bounds() (limit, offset uint, err error) {
	if r.Number == 0 || r.Size == 0 || uint64(r.Size) > uint64(math.MaxInt64) {
		return 0, 0, ErrInvalidRequest
	}

	// Postgres takes OFFSET as a bigint, and the product below wraps silently on
	// a page number no client has any business sending.
	if uint64(r.Number-1) > uint64(math.MaxInt64)/uint64(r.Size) {
		return 0, 0, ErrInvalidRequest
	}

	return r.Size, (r.Number - 1) * r.Size, nil
}

// CursorRequest selects the next rows after a previous response. An empty After
// starts a traversal. Size must be positive; it may change between requests.
type CursorRequest struct {
	Size  uint
	After string
	Sort  []string
}

// CursorResult is a keyset page. NextCursor is present only when HasMore is true.
// It does not include a total; FetchTotal counts the base filter separately.
type CursorResult[T any] struct {
	List       []T    `json:"list"`
	NextCursor string `json:"nextCursor,omitempty"`
	HasMore    bool   `json:"hasMore"`
}
