package pgfx

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uchaloop/pgfx/page"
)

// querier is the minimal surface needed by the fetch helpers. A pool, a
// connection and a transaction all satisfy it.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// collectMany runs the query on q and collects every row with scan.
func collectMany[T any](
	ctx context.Context,
	q querier,
	scan pgx.RowToFunc[T],
	sql string,
	args ...any,
) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, scan)
}

// collectOne runs the query on q and collects exactly one row with scan.
//
// The no-rows error is passed through as pgx reports it. pgx.ErrNoRows wraps
// sql.ErrNoRows, so errors.Is matches it against either sentinel; replacing it
// with the standard one would only take away the ability to match the pgx one.
func collectOne[T any](
	ctx context.Context,
	q querier,
	scan pgx.RowToFunc[T],
	sql string,
	args ...any,
) (T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	return pgx.CollectExactlyOneRow(rows, scan)
}

// collectPage runs the two statements one page takes: the rows, and - only when
// the rows leave it unknown - the count.
func collectPage[T any](
	ctx context.Context,
	q querier,
	query page.Query,
	req page.Request,
	args []any,
) ([]T, uint, error) {
	statements, err := query.BuildRows(reflect.TypeFor[T](), req, len(args))
	if err != nil {
		return nil, 0, err
	}

	rows, err := collectPageRows[T](ctx, q, statements, args)
	if err != nil {
		return nil, 0, err
	}

	// A page shorter than it asked for names the total itself: nothing follows
	// the rows it returned. An empty page says nothing - it may sit past the end
	// of a much larger set - and neither does a full one.
	if n := uint(len(rows)); n > 0 && n < statements.Limit {
		return rows, statements.Offset + n, nil
	}

	count, err := collectTotal(ctx, q, query, args)
	if err != nil {
		return nil, 0, err
	}

	return rows, count, nil
}

// countContext renames the query for the metrics: counting is a second
// statement with a cost profile of its own, and it does not run for every page,
// so reporting it under the caller's name would make both numbers unreadable.
func countContext(ctx context.Context) context.Context {
	name := QueryName(ctx)
	if len(name) == 0 {
		return ctx
	}

	return WithQueryName(ctx, name+".count")
}

// collectPageRows decodes a prepared page without constructing a count query.
func collectPageRows[T any](ctx context.Context, q querier, statements page.Statements, args []any) ([]T, error) {
	return collectMany(ctx, q, pgx.RowToStructByNameLax[T], statements.Rows, append(slices.Clip(args), statements.PagingArgs...)...)
}

func fetchPageRows[T any](ctx context.Context, q querier, query page.Query, req page.Request, args []any) ([]T, error) {
	statements, err := query.BuildRows(reflect.TypeFor[T](), req, len(args))
	if err != nil {
		return nil, err
	}
	return collectPageRows[T](ctx, q, statements, args)
}

func collectTotal(ctx context.Context, q querier, query page.Query, args []any) (uint, error) {
	sql, err := query.BuildCount()
	if err != nil {
		return 0, err
	}
	return collectOne(countContext(ctx), q, pgx.RowTo[uint], sql, args...)
}

func collectAfter[T any](ctx context.Context, q querier, query page.Query, req page.CursorRequest, args []any) (page.CursorResult[T], error) {
	st, err := query.BuildAfter(reflect.TypeFor[T](), req, args...)
	if err != nil {
		return page.CursorResult[T]{}, err
	}
	rows, err := q.Query(ctx, st.Rows, st.Args...)
	if err != nil {
		return page.CursorResult[T]{}, err
	}
	defer rows.Close()
	keyReader, err := newCursorKeyReader(rows.FieldDescriptions(), st.Orders)
	if err != nil {
		return page.CursorResult[T]{}, err
	}
	result := page.CursorResult[T]{List: make([]T, 0, min(req.Size, 256))}
	var keys []any
	for rows.Next() {
		if uint(len(result.List)) == req.Size {
			result.HasMore = true
			break
		}
		item, err := pgx.RowToStructByNameLax[T](rows)
		if err != nil {
			return page.CursorResult[T]{}, err
		}
		result.List = append(result.List, item)
		if uint(len(result.List)) == req.Size {
			keys, err = keyReader.read(rows)
			if err != nil {
				return page.CursorResult[T]{}, err
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page.CursorResult[T]{}, err
	}
	if result.HasMore {
		result.NextCursor, err = st.Cursor(keys)
		if err != nil {
			return page.CursorResult[T]{}, err
		}
	}
	return result, nil
}

// cursorKeyReader selects only ordered columns. Non-key columns are skipped by
// pgx, so large JSON payloads are not decoded again for cursor construction.
type cursorKeyReader struct {
	positions   []int
	columnCount int
}

func newCursorKeyReader(fields []pgconn.FieldDescription, orders []page.Order) (cursorKeyReader, error) {
	reader := cursorKeyReader{positions: make([]int, len(orders)), columnCount: len(fields)}
	for i, order := range orders {
		reader.positions[i] = -1
		for j, field := range fields {
			if field.Name != order.Column {
				continue
			}
			if reader.positions[i] != -1 {
				return cursorKeyReader{}, fmt.Errorf("%w: duplicate column %q", page.ErrInvalidColumn, order.Column)
			}
			reader.positions[i] = j
		}
		if reader.positions[i] == -1 {
			return cursorKeyReader{}, fmt.Errorf("%w: missing column %q", page.ErrInvalidColumn, order.Column)
		}
	}
	return reader, nil
}

func (r cursorKeyReader) read(rows pgx.Rows) ([]any, error) {
	keys := make([]any, len(r.positions))
	dest := make([]any, r.columnCount)
	for i, pos := range r.positions {
		dest[pos] = &keys[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	// Detach byte keys before Next can invalidate a custom codec's row buffer.
	for i, key := range keys {
		if value, ok := key.([]byte); ok {
			keys[i] = slices.Clone(value)
		}
	}
	return keys, nil
}
