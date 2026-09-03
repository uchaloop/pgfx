package pgfx

import (
	"context"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
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
	statements, err := query.Build(reflect.TypeFor[T](), req, len(args))
	if err != nil {
		return nil, 0, err
	}

	rows, err := collectMany(
		ctx, q, pgx.RowToStructByNameLax[T], statements.Rows,
		append(slices.Clip(args), statements.Limit, statements.Offset)...,
	)
	if err != nil {
		return nil, 0, err
	}

	// A page shorter than it asked for names the total itself: nothing follows
	// the rows it returned. An empty page says nothing - it may sit past the end
	// of a much larger set - and neither does a full one.
	if n := uint(len(rows)); n > 0 && n < statements.Limit {
		return rows, statements.Offset + n, nil
	}

	count, err := collectOne(countContext(ctx), q, pgx.RowTo[uint], statements.Count, args...)
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
