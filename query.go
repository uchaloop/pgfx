package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5"
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
