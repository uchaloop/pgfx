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

func wrapTx(tx pgx.Tx) *Tx {
	return &Tx{Tx: tx}
}

// FetchRows runs the query and decodes every row into a struct T. Columns map to
// struct fields by the `db:"..."` tag, falling back to the field name. For a
// single-column result use FetchValues.
func (t *Tx) FetchRows[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(ctx, t.Tx, pgx.RowToStructByNameLax[T], sql, args...)
}

// FetchRow runs the query and decodes exactly one struct row into T. It reports
// pgx.ErrNoRows when the query yields no rows and pgx.ErrTooManyRows when it
// yields more than one. For a scalar result use FetchValue.
func (t *Tx) FetchRow[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(ctx, t.Tx, pgx.RowToStructByNameLax[T], sql, args...)
}

// FetchValues runs the query and decodes a single-column result into a slice of
// scalar T (int64, string, uuid.UUID, ...). For struct rows use FetchRows.
func (t *Tx) FetchValues[T any](ctx context.Context, sql string, args ...any) ([]T, error) {
	return collectMany(ctx, t.Tx, pgx.RowTo[T], sql, args...)
}

// FetchValue runs the query and decodes exactly one single-column row into a
// scalar T. It reports pgx.ErrNoRows when the query yields no rows and
// pgx.ErrTooManyRows when it yields more than one. For a struct use FetchRow.
func (t *Tx) FetchValue[T any](ctx context.Context, sql string, args ...any) (T, error) {
	return collectOne(ctx, t.Tx, pgx.RowTo[T], sql, args...)
}

// FetchPage runs a paginated query and decodes one page of struct rows into T,
// with the number of rows the filter matches in total. It is [DB.FetchPage]
// inside a transaction, where the rows and the count see one snapshot - under a
// repeatable read isolation level, a total that cannot disagree with the page it
// describes.
func (t *Tx) FetchPage[T any](
	ctx context.Context,
	query page.Query,
	req page.Request,
	args ...any,
) ([]T, uint, error) {
	return collectPage[T](ctx, t.Tx, query, req, args)
}

// BeginNested starts a pseudo-nested transaction implemented by pgx with a
// savepoint. The caller must call Commit or Rollback. Transaction is the safer
// callback-based alternative.
func (t *Tx) BeginNested(ctx context.Context) (*Tx, error) {
	tx, err := t.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}

	return wrapTx(tx), nil
}

// Transaction runs fn in a pseudo-nested transaction implemented by pgx with a
// savepoint. A nil error releases the savepoint; a non-nil error rolls back to
// it. A panic rolls back to it and continues unwinding.
func (t *Tx) Transaction(ctx context.Context, fn func(*Tx) error) error {
	return pgx.BeginFunc(ctx, t.Tx, func(tx pgx.Tx) error {
		return fn(wrapTx(tx))
	})
}
