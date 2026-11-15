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

// collectPageRows decodes a prepared page using the requested capacity.
func collectPageRows[T any](ctx context.Context, q querier, statement page.Statement, args []any) ([]T, error) {
	return collectInto(
		ctx,
		q,
		make([]T, 0, statement.Limit),
		pgx.RowToStructByNameLax[T],
		statement.Rows,
		append(slices.Clip(args), statement.PageArgs...)...,
	)
}

func collectPage[T any](ctx context.Context, q querier, query page.Query, req page.Request, args []any) (page.Result[T], error) {
	statement, err := query.BuildStatement(reflect.TypeFor[T](), req, len(args))
	if err != nil {
		return page.Result[T]{}, err
	}

	rows, err := collectPageRows[T](ctx, q, statement, args)
	if err != nil {
		return page.Result[T]{}, err
	}

	total, err := collectOne(ctx, q, pgx.RowTo[int64], statement.Count, args...)
	if err != nil {
		return page.Result[T]{}, err
	}

	return page.Result[T]{List: rows, Total: total}, nil
}

func collectOptionalRow[T any](ctx context.Context, q querier, sql string, args ...any) (T, bool, error) {
	var zero T

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return zero, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return zero, false, rows.Err()
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
