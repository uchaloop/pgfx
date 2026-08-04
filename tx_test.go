package pgfx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeTxBeginner struct {
	tx   pgx.Tx
	err  error
	opts pgx.TxOptions
}

func (db *fakeTxBeginner) BeginTx(
	_ context.Context,
	opts pgx.TxOptions,
) (pgx.Tx, error) {
	db.opts = opts

	return db.tx, db.err
}

type fakeTx struct {
	pgx.Tx
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (tx *fakeTx) Commit(context.Context) error {
	tx.commitCalls++

	return tx.commitErr
}

func (tx *fakeTx) Rollback(context.Context) error {
	tx.rollbackCalls++

	return tx.rollbackErr
}

func TestTxCommitsSuccessfulCallback(t *testing.T) {
	tx := &fakeTx{}
	db := &fakeTxBeginner{tx: tx}
	opts := pgx.TxOptions{IsoLevel: pgx.Serializable}
	var received pgx.Tx

	err := Tx(context.Background(), db, opts, func(got pgx.Tx) error {
		received = got

		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if received != tx {
		t.Fatal("callback received a different transaction")
	}
	if db.opts != opts {
		t.Fatalf("BeginTx options = %#v, want %#v", db.opts, opts)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/0", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestTxRollsBackCallbackError(t *testing.T) {
	tx := &fakeTx{}
	db := &fakeTxBeginner{tx: tx}
	want := errors.New("callback failed")

	err := Tx(context.Background(), db, pgx.TxOptions{}, func(pgx.Tx) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Tx error = %v, want %v", err, want)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 0/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestTxWrapsBeginError(t *testing.T) {
	want := errors.New("begin failed")
	db := &fakeTxBeginner{err: want}

	err := Tx(context.Background(), db, pgx.TxOptions{}, func(pgx.Tx) error {
		t.Fatal("callback called after BeginTx error")

		return nil
	})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "begin tx") {
		t.Fatalf("Tx error = %v, want wrapped begin error", err)
	}
}

func TestTxWrapsCommitError(t *testing.T) {
	want := errors.New("commit failed")
	tx := &fakeTx{commitErr: want}

	err := Tx(
		context.Background(),
		&fakeTxBeginner{tx: tx},
		pgx.TxOptions{},
		func(pgx.Tx) error { return nil },
	)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "commit tx") {
		t.Fatalf("Tx error = %v, want wrapped commit error", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/0", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestTxRollsBackAndRepanics(t *testing.T) {
	tx := &fakeTx{}
	const want = "panic value"

	defer func() {
		if got := recover(); got != want {
			t.Fatalf("panic = %#v, want %#v", got, want)
		}
		if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
			t.Fatalf(
				"commit/rollback calls = %d/%d, want 0/1",
				tx.commitCalls,
				tx.rollbackCalls,
			)
		}
	}()

	_ = Tx(
		context.Background(),
		&fakeTxBeginner{tx: tx},
		pgx.TxOptions{},
		func(pgx.Tx) error { panic(want) },
	)
}
