package page

import (
	"errors"
	"math"
)

// ErrBounds reports a page number or size below 1, or an offset that
// overflows int64.
var ErrBounds = errors.New("invalid page bounds")

// Bounds holds the LIMIT and OFFSET of one page.
type Bounds struct {
	Limit  int64
	Offset int64
}

// Result is one page of items and the number of items matching the query
// across all pages. List is non-nil on success, even for an empty page.
type Result[T any] struct {
	List  []T
	Total int64
}

// MakeBounds converts a 1-based page number and a page size into LIMIT/OFFSET
// values. It applies no defaults and no maximum: validate both values before
// calling it, the maximum size included.
func MakeBounds(number, size int) (Bounds, error) {
	if number < 1 || size < 1 {
		return Bounds{}, ErrBounds
	}

	limit := int64(size)
	if int64(number-1) > math.MaxInt64/limit {
		return Bounds{}, ErrBounds
	}

	return Bounds{Limit: limit, Offset: int64(number-1) * limit}, nil
}
