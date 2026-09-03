package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/pgfx/page"
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

// FetchPage runs a paginated query and decodes one page of struct rows into T,
// with the number of rows the filter matches in total. The page request carries
// the page number, its size and the client's sort; the rest of the arguments are
// the query's own, numbered from $1.
//
// The page is applied around the query - ORDER BY, LIMIT and OFFSET are wrapped
// on the outside - so the query stays a plain SELECT and its columns decode by
// the `db:"..."` tag as everywhere else. The total takes a second statement,
// which is skipped whenever the returned rows already imply it. An empty page is
// a valid result, not an error.
func (d *DB) FetchPage[T any](
	ctx context.Context,
	query page.Query,
	req page.Request,
	args ...any,
) ([]T, uint, error) {
	return collectPage[T](ctx, d.Pool, query, req, args)
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
