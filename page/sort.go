package page

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSort reports an unknown or repeated sort key.
var ErrSort = errors.New("invalid sort key")

// Key is one requested sort key. K is usually a string or the application's
// own string-based enum type.
type Key[K comparable] struct {
	Name K
	Desc bool
}

// Sortable maps request sort keys to trusted SQL expressions. Only these
// expressions reach the ORDER BY clause; request values never do.
type Sortable[K comparable] map[K]string

// OrderBy returns the expression list for ORDER BY, without the keyword, for
// example "o.partner_name DESC, o.id DESC". Keys are applied in order.
//
// tie must be a unique column, such as the primary key: it makes the order
// stable across pages. It is appended with the direction of the last key, or
// ASC when keys is empty, so that one composite index can serve the whole
// order. It is not appended when a key already resolves to it.
//
// An unknown or repeated key returns an error matching ErrSort. NULL
// placement follows PostgreSQL defaults: last for ASC, first for DESC. The
// direction is appended to the mapped expression, so an expression must not
// end with NULLS FIRST or NULLS LAST. To keep NULLs last in both directions,
// map the key to "(expr IS NULL), expr"; use IS NOT NULL to keep them first.
func (s Sortable[K]) OrderBy(keys []Key[K], tie string) (string, error) {
	if len(tie) == 0 {
		return "", errors.New("sort tie is empty")
	}

	items := make([]string, 0, len(keys)+1)
	seen := make(map[K]struct{}, len(keys))
	direction := "ASC"
	hasTie := false

	for _, key := range keys {
		expr, ok := s[key.Name]
		if !ok {
			return "", fmt.Errorf("%w: %v", ErrSort, key.Name)
		}

		if _, ok := seen[key.Name]; ok {
			return "", fmt.Errorf("%w: %v is repeated", ErrSort, key.Name)
		}

		seen[key.Name] = struct{}{}

		direction = "ASC"
		if key.Desc {
			direction = "DESC"
		}

		items = append(items, expr+" "+direction)
		hasTie = hasTie || expr == tie
	}

	if !hasTie {
		items = append(items, tie+" "+direction)
	}

	return strings.Join(items, ", "), nil
}
