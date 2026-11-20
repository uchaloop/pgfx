package page

import (
	"errors"
	"math"
	"strings"
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

// Limits is an immutable page size policy.
type Limits struct {
	minSize, maxSize, defaultSize int
	maxOffset                     int64
	isOffsetLimited               bool
}

// MakeLimits requires 1 <= minSize <= defaultSize <= maxSize.
func MakeLimits(minSize, maxSize, defaultSize int) (Limits, error) {
	if minSize < 1 || defaultSize < minSize || maxSize < defaultSize {
		return Limits{}, errors.New("invalid page size policy")
	}

	return Limits{minSize: minSize, maxSize: maxSize, defaultSize: defaultSize}, nil
}

// WithMaxOffset returns a copy with an inclusive offset ceiling. Zero permits
// only the first page. Limits made by MakeLimits have no offset ceiling.
func (l Limits) WithMaxOffset(maxOffset int64) (Limits, error) {
	if l.minSize == 0 || maxOffset < 0 {
		return Limits{}, ErrBounds
	}

	l.maxOffset = maxOffset
	l.isOffsetLimited = true
	return l, nil
}

// Bounds uses page 1 for numbers below 1 and the default for sizes below 1.
// Positive sizes are clamped before computing the offset.
func (l Limits) Bounds(number, size int) (Bounds, error) {
	if l.minSize == 0 {
		return Bounds{}, ErrBounds
	}

	if size < 1 {
		size = l.defaultSize
	}

	b, err := MakeBounds(max(1, number), min(l.maxSize, max(l.minSize, size)))
	if err != nil {
		return Bounds{}, err
	}
	if l.isOffsetLimited && b.Offset > l.maxOffset {
		return Bounds{}, ErrBounds
	}

	return b, nil
}

// SortFields maps request names to trusted SQL expressions.
type SortFields map[string]string

// SortConfig defines allowed sorts and the trusted unique tie column.
type SortConfig struct {
	Fields SortFields
	Tie    string
}

// Pager holds immutable list settings. Its Resolve method is safe for concurrent use.
type Pager struct {
	limits Limits
	fields Sortable[string]
	tie    string
}

// MakePager validates the configuration and copies the field mapping.
// A zero Limits returns ErrBounds. Empty names, expressions or tie columns,
// and duplicate normalized field names return ErrSort.
func MakePager(l Limits, c SortConfig) (Pager, error) {
	if l.minSize == 0 {
		return Pager{}, ErrBounds
	}
	if len(strings.TrimSpace(c.Tie)) == 0 {
		return Pager{}, ErrSort
	}

	fields := make(Sortable[string], len(c.Fields))
	for name, expr := range c.Fields {
		k := strings.ToLower(strings.TrimSpace(name))
		if _, isKnown := fields[k]; isKnown || len(k) == 0 || len(strings.TrimSpace(expr)) == 0 {
			return Pager{}, ErrSort
		}

		fields[k] = expr
	}

	return Pager{limits: l, fields: fields, tie: c.Tie}, nil
}

// Resolve chooses the first recognized field. Only desc selects descending order.
// An offset overflow or a zero Pager returns ErrBounds.
func (p Pager) Resolve(number, size int, values []string) (Selection, error) {
	b, err := p.limits.Bounds(number, size)
	if err != nil {
		return Selection{}, err
	}

	var keys []Key[string]
	for _, v := range values {
		name, dir, _ := strings.Cut(v, ":")
		k := strings.ToLower(strings.TrimSpace(name))
		if _, isKnown := p.fields[k]; !isKnown {
			continue
		}

		keys = []Key[string]{{Name: k, Desc: strings.EqualFold(strings.TrimSpace(dir), "desc")}}
		break
	}

	order, err := p.fields.OrderBy(keys, p.tie)
	if err != nil {
		return Selection{}, err
	}

	return Selection{bounds: b, order: order}, nil
}

// Spec supplies bounds and an optional trusted order. FetchPage accepts Bounds and Selection.
type Spec interface {
	pageSpec()
	PageParts() (Bounds, string)
}

func (Bounds) pageSpec() {}

// PageParts returns bounds for a query with its own ORDER BY.
func (b Bounds) PageParts() (Bounds, string) { return b, "" }

// Selection is the result of resolving a request against a Pager.
type Selection struct {
	bounds Bounds
	order  string
}

func (Selection) pageSpec() {}

// PageParts returns the normalized bounds and trusted order expression list.
func (s Selection) PageParts() (Bounds, string) { return s.bounds, s.order }
