package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uchaloop/pgfx/page"
)

// DB extends a PostgreSQL pool with typed queries.
// MakeDB returns an initialized DB owned by its caller.
// Fx provides DB during graph construction and initializes Pool in OnStart.
// Consumers query in their own OnStart hooks or after application startup,
// finish work in OnStop, and must not close the lifecycle-owned pool themselves.
// Once initialized, DB supports concurrent queries. Its zero value is not usable;
// Pool must not be replaced while DB is in use.
type DB struct {
	*pgxpool.Pool

	state *connState
}

// FetchRows runs the query and decodes every row into a struct T. Columns map to
// struct fields by the `db:"..."` tag, falling back to the field name. For a
// single-column result use FetchValues.
func (d *DB) FetchRows[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(
		ctx,
		d.Pool,
		pgx.RowToStructByNameLax[T],
		sql,
		args...,
	)
}

// FetchRowsInto appends struct rows to dst. Pass dst[:0] to reuse its buffer.
// Capacity is a reservation, not a row limit; the slice grows as needed.
// On error it returns nil; appended values may already have modified dst's
// backing array. Do not reuse that array while another consumer is reading it.
func (d *DB) FetchRowsInto[T any](ctx context.Context, dst []T, sql string, args ...any) ([]T, error) {
	return collectInto(
		ctx,
		d.Pool,
		dst,
		pgx.RowToStructByNameLax[T],
		sql,
		args...,
	)
}

// FetchValuesInto appends single-column values to dst. Pass dst[:0] to reuse its buffer.
// Capacity is a reservation, not a row limit; the slice grows as needed.
// On error it returns nil; appended values may already have modified dst's
// backing array. Do not reuse that array while another consumer is reading it.
func (d *DB) FetchValuesInto[T any](ctx context.Context, dst []T, sql string, args ...any) ([]T, error) {
	return collectInto(
		ctx,
		d.Pool,
		dst,
		pgx.RowTo[T],
		sql,
		args...,
	)
}

// FetchRow runs the query and decodes exactly one struct row into T. It reports
// pgx.ErrNoRows when the query yields no rows and pgx.ErrTooManyRows when it
// yields more than one. For a scalar result use FetchValue.
func (d *DB) FetchRow[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(
		ctx,
		d.Pool,
		pgx.RowToStructByNameLax[T],
		sql,
		args...,
	)
}

// FetchValues runs the query and decodes a single-column result into a slice of
// scalar T (int64, string, uuid.UUID, ...). For struct rows use FetchRows.
func (d *DB) FetchValues[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(
		ctx,
		d.Pool,
		pgx.RowTo[T],
		sql,
		args...,
	)
}

// FetchValue runs the query and decodes exactly one single-column row into a
// scalar T. It reports pgx.ErrNoRows when the query yields no rows and
// pgx.ErrTooManyRows when it yields more than one. For a struct use FetchRow.
func (d *DB) FetchValue[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(
		ctx,
		d.Pool,
		pgx.RowTo[T],
		sql,
		args...,
	)
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
	return pgx.BeginTxFunc(
		ctx,
		d.Pool,
		opts,
		func(tx pgx.Tx) error {
			return fn(makeTx(tx))
		},
	)
}

// TransactionValue runs fn in a transaction and returns its value only after
// a successful commit. Any error returns the zero value of T and the transaction
// error unchanged. Callback, rollback and panic semantics match Transaction.
func (d *DB) TransactionValue[T any](ctx context.Context, opts pgx.TxOptions, fn func(*Tx) (T, error)) (T, error) {
	return transactionValue(func(callback func(*Tx) error) error {
		return d.Transaction(ctx, opts, callback)
	}, fn)
}

// FetchOptionalRow decodes at most one struct row, using the same mapping as
// FetchRow. An empty result returns the zero value of T, false and nil.
// Exactly one decoded row returns its value, true and nil. Multiple rows return
// pgx.ErrTooManyRows. Query, decoding and iteration errors are returned unchanged
// with a zero value and false, including errors that match pgx.ErrNoRows.
// FetchRow retains its strict exactly-one-row contract.
func (d *DB) FetchOptionalRow[T any](ctx context.Context, sql string, args ...any) (T, bool, error) {
	return collectOptionalRow[T](
		ctx,
		d.Pool,
		sql,
		args...,
	)
}

// FetchPage runs one page query and returns its rows with the number of rows
// the query matches across all pages. Rows map to T as in FetchRows.
//
// Pass page.Bounds for a query with its own ORDER BY. For a page.Selection,
// use ORDER BY @order and supply only filter values in args.
//
// The query must reference @limit and @offset and end with a bigint column
// named total, usually count(*) OVER () AS total. FetchPage binds the page size to @limit
// and the offset to @offset; args must not set them. When the page lies past the end of a
// non-empty result, FetchPage runs the query once more with offset 0 to read
// the total. List is non-nil on success.
//
// The window total is evaluated before DISTINCT, so the query must not use
// SELECT DISTINCT; use GROUP BY instead. Invalid bounds return page.ErrBounds
// before the query runs. A nil spec or a typed nil pointer also returns page.ErrBounds.
func (d *DB) FetchPage[T any](ctx context.Context, sql string, args pgx.NamedArgs, spec page.Spec) (page.Result[T], error) {
	return collectPage[T](
		ctx,
		d.Pool,
		sql,
		args,
		spec,
	)
}
