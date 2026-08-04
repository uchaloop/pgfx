package pgfx

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TxBeginner starts transactions. It is satisfied by *pgxpool.Pool and *pgx.Conn,
// so Tx runs on a pool or a single connection alike - the transaction-level
// counterpart of Querier for the fetch helpers.
type TxBeginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Tx runs fn inside a transaction begun on db and commits it, or rolls back on
// error or panic. The transaction is passed explicitly to fn (no ambient
// transaction via context): because pgx.Tx satisfies Querier, FetchRow/FetchRows
// work on it unchanged.
//
// opts selects the isolation level (e.g. pgx.TxOptions{IsoLevel: pgx.Serializable}).
//
// Tx does not retry serialization failures (SQLSTATE 40001) and does not nest:
// calling Tx from within fn opens a second top-level transaction on a different
// connection, not a savepoint. Handle both concerns at the call site if needed.
func Tx(
	ctx context.Context,
	db TxBeginner,
	opts pgx.TxOptions,
	fn func(pgx.Tx) error,
) (err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()

	if err = fn(tx); err != nil {
		_ = tx.Rollback(ctx) // best-effort; tx may already be aborted

		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	return nil
}
