package pgfx

import (
	"context"
	stdsql "database/sql" // aliased: the Fetch* helpers take a param named "sql"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Querier is the minimal query surface the generic fetch helpers need. It is
// satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx alike, so the helpers run
// unchanged both directly on a pool and inside a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// FetchRows runs the query on q and decodes every row into a struct T. Columns
// map to struct fields by the `db:"..."` tag (falling back to the field name).
// For a single-column (scalar) result use FetchValues.
func FetchRows[T any](
	ctx context.Context,
	q Querier,
	sql string,
	args ...any,
) ([]T, error) {
	return collectMany(ctx, q, pgx.RowToStructByNameLax[T], sql, args...)
}

// FetchRow runs the query on q and decodes exactly one struct row into T. It
// returns sql.ErrNoRows when the query yields no rows, and pgx.ErrTooManyRows
// when it yields more than one. For a scalar result use FetchValue.
func FetchRow[T any](
	ctx context.Context,
	q Querier,
	sql string,
	args ...any,
) (T, error) {
	return collectOne(ctx, q, pgx.RowToStructByNameLax[T], sql, args...)
}

// FetchValues runs the query on q and decodes a single-column result into a
// slice of scalar T (int64, string, uuid.UUID, ...). For struct rows use
// FetchRows.
func FetchValues[T any](
	ctx context.Context,
	q Querier,
	sql string,
	args ...any,
) ([]T, error) {
	return collectMany(ctx, q, pgx.RowTo[T], sql, args...)
}

// FetchValue runs the query on q and decodes exactly one single-column row into
// a scalar T. It is the helper for count(*), INSERT ... RETURNING id and similar.
// It returns sql.ErrNoRows when the query yields no rows, and pgx.ErrTooManyRows
// when it yields more than one. For struct rows use FetchRow.
func FetchValue[T any](
	ctx context.Context,
	q Querier,
	sql string,
	args ...any,
) (T, error) {
	return collectOne(ctx, q, pgx.RowTo[T], sql, args...)
}

// collectMany runs the query on q and collects every row with scan.
func collectMany[T any](
	ctx context.Context,
	q Querier,
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

// collectOne runs the query on q, collects exactly one row with scan, and maps
// pgx's no-rows error to the standard sql.ErrNoRows.
func collectOne[T any](
	ctx context.Context,
	q Querier,
	scan pgx.RowToFunc[T],
	sql string,
	args ...any,
) (T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	v, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, stdsql.ErrNoRows
	}

	return v, err
}
