package pgfx

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

// Tx is a Postgres transaction with the same generic fetch methods as DB. It
// embeds the native pgx transaction API and adds typed fetch helpers.
//
// A Tx is not safe for concurrent use. Its zero value is not usable.
type Tx struct {
	pgx.Tx
}

func makeTx(tx pgx.Tx) *Tx {
	return &Tx{Tx: tx}
}

// FetchRows runs the query and decodes every row into a struct T. Columns map to
// struct fields by the `db:"..."` tag, falling back to the field name. For a
// single-column result use FetchValues.
func (t *Tx) FetchRows[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(
		ctx,
		t.Tx,
		pgx.RowToStructByNameLax[T],
		sql,
		args...,
	)
}

// FetchRowsInto appends struct rows to dst. Pass dst[:0] to reuse its buffer.
// Capacity is a reservation, not a row limit; the slice grows as needed.
// On error it returns nil; appended values may already have modified dst's
// backing array. Do not reuse that array while another consumer is reading it.
func (t *Tx) FetchRowsInto[T any](ctx context.Context, dst []T, sql string, args ...any) ([]T, error) {
	return collectInto(
		ctx,
		t.Tx,
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
func (t *Tx) FetchValuesInto[T any](ctx context.Context, dst []T, sql string, args ...any) ([]T, error) {
	return collectInto(
		ctx,
		t.Tx,
		dst,
		pgx.RowTo[T],
		sql,
		args...,
	)
}

// FetchRow runs the query and decodes exactly one struct row into T. It reports
// pgx.ErrNoRows when the query yields no rows and pgx.ErrTooManyRows when it
// yields more than one. For a scalar result use FetchValue.
func (t *Tx) FetchRow[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(
		ctx,
		t.Tx,
		pgx.RowToStructByNameLax[T],
		sql,
		args...,
	)
}

// FetchValues runs the query and decodes a single-column result into a slice of
// scalar T (int64, string, uuid.UUID, ...). For struct rows use FetchRows.
func (t *Tx) FetchValues[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(
		ctx,
		t.Tx,
		pgx.RowTo[T],
		sql,
		args...,
	)
}

// FetchValue runs the query and decodes exactly one single-column row into a
// scalar T. It reports pgx.ErrNoRows when the query yields no rows and
// pgx.ErrTooManyRows when it yields more than one. For a struct use FetchRow.
func (t *Tx) FetchValue[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(
		ctx,
		t.Tx,
		pgx.RowTo[T],
		sql,
		args...,
	)
}

// FetchPage returns one numbered page and the total number of base query rows.
// Page is 1-based. It runs a page SELECT followed by COUNT, even for an empty page.
// No transaction is created; use a repeatable-read transaction when both queries
// must observe the same snapshot. Any error returns a zero result.
func (t *Tx) FetchPage[T any](ctx context.Context, query page.Query, req page.Request, args ...any) (page.Result[T], error) {
	return collectPage[T](
		ctx,
		t.Tx,
		query,
		req,
		args,
	)
}

// BeginNested starts a pseudo-nested transaction implemented by pgx with a
// savepoint. The caller must call Commit or Rollback. Transaction is the safer
// callback-based alternative.
func (t *Tx) BeginNested(ctx context.Context) (*Tx, error) {
	tx, err := t.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}

	return makeTx(tx), nil
}

// Transaction runs fn in a pseudo-nested transaction implemented by pgx with a
// savepoint. A nil error releases the savepoint; a non-nil error rolls back to
// it. A panic rolls back to it and continues unwinding.
func (t *Tx) Transaction(ctx context.Context, fn func(*Tx) error) error {
	return pgx.BeginFunc(ctx, t.Tx, func(tx pgx.Tx) error {
		return fn(makeTx(tx))
	})
}

// TransactionValue runs fn in a savepoint and returns its value only after
// the savepoint is released. This does not commit the outer transaction.
// Any error returns the zero value of T and the transaction error unchanged.
// Callback, rollback and panic semantics match Transaction.
func (t *Tx) TransactionValue[T any](ctx context.Context, fn func(*Tx) (T, error)) (T, error) {
	return transactionValue(func(callback func(*Tx) error) error {
		return t.Transaction(ctx, callback)
	}, fn)
}

// FetchOptionalRow decodes at most one struct row, using the same mapping as
// FetchRow. An empty result returns the zero value of T, false and nil.
// Exactly one decoded row returns its value, true and nil. Multiple rows return
// pgx.ErrTooManyRows. Query, decoding and iteration errors are returned unchanged
// with a zero value and false, including errors that match pgx.ErrNoRows.
// FetchRow retains its strict exactly-one-row contract.
func (t *Tx) FetchOptionalRow[T any](ctx context.Context, sql string, args ...any) (T, bool, error) {
	return collectOptionalRow[T](
		ctx,
		t.Tx,
		sql,
		args...,
	)
}
