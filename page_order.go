package pgfx

import (
	"errors"
	"iter"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

// preparePage binds filter names once; the first two positions are page bounds.
func preparePage(sql, order string, args pgx.NamedArgs) (string, func(page.Bounds) []any, error) {
	var out strings.Builder
	last := 0
	seen := make(map[string]bool)
	indexes := map[string]int{"limit": 1, "offset": 2}
	values := []any{nil, nil}

	for i, j := range pageMarkers(sql) {
		name := sql[i+1 : j]
		out.WriteString(sql[last:i])

		if name == "order" {
			if seen[name] {
				return "", nil, errors.New("duplicate page order marker")
			}

			out.WriteString(order)
		} else {
			idx, isIndexed := indexes[name]
			if !isIndexed {
				v, isSet := args[name]
				if !isSet {
					return "", nil, errors.New("missing page query argument")
				}

				values = append(values, v)
				idx = len(values)
				indexes[name] = idx
			}

			out.WriteByte('$')
			out.WriteString(strconv.Itoa(idx))
		}

		seen[name] = true
		last = j
	}

	if !seen["order"] || !seen["limit"] || !seen["offset"] {
		return "", nil, errors.New("page query must use @order, @limit and @offset")
	}

	out.WriteString(sql[last:])

	return out.String(), func(b page.Bounds) []any {
		values[0], values[1] = b.Limit, b.Offset
		return values
	}, nil
}

// pageMarkers yields byte ranges of named parameters outside literals and comments.
func pageMarkers(sql string) iter.Seq2[int, int] {
	return func(yield func(int, int) bool) {
		for i := 0; i < len(sql); {
			switch {
			case strings.HasPrefix(sql[i:], "--"):
				n := strings.IndexAny(sql[i:], "\r\n")
				if n < 0 {
					i = len(sql)
				} else {
					i += n + 1
				}
			case strings.HasPrefix(sql[i:], "/*"):
				i = skipPageComment(sql, i)
			case sql[i] == '\'' || sql[i] == '"':
				i = skipPageQuote(sql, i)
			case sql[i] == '$' && (i == 0 || !isPageIdentPart(sql[i-1])):
				i = skipPageDollarQuote(sql, i)
			case sql[i] == '@' && i+1 < len(sql) && isPageIdentStart(sql[i+1]):
				j := i + 2
				for j < len(sql) && isPageIdentPart(sql[j]) {
					j++
				}

				if !yield(i, j) {
					return
				}
				i = j
			default:
				i++
			}
		}
	}
}

func hasPageBounds(sql string) bool {
	var isLimitSet, isOffsetSet bool
	for start, end := range pageMarkers(sql) {
		switch sql[start+1 : end] {
		case "limit":
			isLimitSet = true
		case "offset":
			isOffsetSet = true
		}
		if isLimitSet && isOffsetSet {
			return true
		}
	}
	return false
}

func skipPageComment(sql string, i int) int {
	i += 2

	for depth := 1; depth > 0 && i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2
		default:
			i++
		}
	}

	return i
}

func skipPageQuote(sql string, i int) int {
	quote := sql[i]
	isEscaped := quote == '\'' && i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i == 1 || !isPageIdentPart(sql[i-2]))
	i++

	for i < len(sql) {
		if isEscaped && sql[i] == '\\' {
			i = min(i+2, len(sql))
			continue
		}

		c := sql[i]
		i++
		if c != quote {
			continue
		}
		if i < len(sql) && sql[i] == quote {
			i++
			continue
		}
		break
	}

	return i
}

func skipPageDollarQuote(sql string, i int) int {
	j := i + 1
	if j < len(sql) && isPageIdentStart(sql[j]) {
		for j < len(sql) && (isPageIdentStart(sql[j]) || sql[j] >= '0' && sql[j] <= '9') {
			j++
		}
	}

	if j == len(sql) || sql[j] != '$' {
		return i + 1
	}

	tag := sql[i : j+1]
	n := strings.Index(sql[j+1:], tag)
	if n < 0 {
		return len(sql)
	}

	return j + 1 + n + len(tag)
}

func isPageIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 128
}

func isPageIdentPart(c byte) bool { return isPageIdentStart(c) || c >= '0' && c <= '9' || c == '$' }
