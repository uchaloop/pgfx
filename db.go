package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is a Postgres connection: a pool, and the queries bound to it. It is what
// Make returns and what the Fx modules provide, so a repository depends on a DB
// rather than on a pool plus a set of free functions.
//
// A DB is safe for concurrent use, as the pool behind it is. Its zero value is
// not usable.
type DB struct {
	*pgxpool.Pool
}

// FetchRows runs the query and decodes every row into a struct T. Columns map to
// struct fields by the `db:"..."` tag, falling back to the field name. For a
// single-column result use FetchValues.
func (d *DB) FetchRows[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(ctx, d.Pool, pgx.RowToStructByNameLax[T], sql, args...)
}

// FetchRow runs the query and decodes exactly one struct row into T. It reports
// pgx.ErrNoRows when the query yields no rows and pgx.ErrTooManyRows when it
// yields more than one. For a scalar result use FetchValue.
func (d *DB) FetchRow[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(ctx, d.Pool, pgx.RowToStructByNameLax[T], sql, args...)
}

// FetchValues runs the query and decodes a single-column result into a slice of
// scalar T (int64, string, uuid.UUID, ...). For struct rows use FetchRows.
func (d *DB) FetchValues[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(ctx, d.Pool, pgx.RowTo[T], sql, args...)
}

// FetchValue runs the query and decodes exactly one single-column row into a
// scalar T. It reports pgx.ErrNoRows when the query yields no rows and
// pgx.ErrTooManyRows when it yields more than one. For a struct use FetchRow.
func (d *DB) FetchValue[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(ctx, d.Pool, pgx.RowTo[T], sql, args...)
}

// Transaction runs fn in a transaction. A nil error commits; a non-nil error
// rolls back and is returned. A panic rolls back and continues unwinding. Use
// the callback's Tx for every transactional operation; calls through d use the
// pool and are outside this transaction.
func (d *DB) Transaction(
	ctx context.Context,
	opts pgx.TxOptions,
	fn func(*Tx) error,
) error {
	return pgx.BeginTxFunc(ctx, d.Pool, opts, func(tx pgx.Tx) error {
		return fn(wrapTx(tx))
	})
}
