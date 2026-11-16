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
	return collectInto(
		ctx,
		q,
		[]T{},
		scan,
		sql,
		args...,
	)
}

// collectInto appends decoded rows to dst, preserving the caller's capacity.
func collectInto[T any](ctx context.Context, q querier, dst []T, scan pgx.RowToFunc[T], sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}

	return pgx.AppendRows(dst, rows, scan)
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

func collectOptionalRow[T any](ctx context.Context, q querier, sql string, args ...any) (T, bool, error) {
	var zero T

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return zero, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, false, err
		}

		return zero, false, nil
	}

	value, err := pgx.RowToStructByNameLax[T](rows)
	if err != nil {
		return zero, false, err
	}

	if rows.Next() {
		return zero, false, pgx.ErrTooManyRows
	}

	if err := rows.Err(); err != nil {
		return zero, false, err
	}

	return value, true, nil
}
