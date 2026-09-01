package pgfx

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

var _ pgx.Tx = (*Tx)(nil)

type fakeNativeTx struct {
	pgx.Tx
	rows          pgx.Rows
	queryErr      error
	nested        pgx.Tx
	beginErr      error
	beginCalls    int
	commitCalls   int
	rollbackCalls int
	closed        bool
}

func (tx *fakeNativeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return tx.rows, tx.queryErr
}

func (tx *fakeNativeTx) Begin(context.Context) (pgx.Tx, error) {
	tx.beginCalls++

	return tx.nested, tx.beginErr
}

func (tx *fakeNativeTx) Commit(context.Context) error {
	if tx.closed {
		return pgx.ErrTxClosed
	}

	tx.closed = true
	tx.commitCalls++

	return nil
}

func (tx *fakeNativeTx) Rollback(context.Context) error {
	if tx.closed {
		return pgx.ErrTxClosed
	}

	tx.closed = true
	tx.rollbackCalls++

	return nil
}

func TestTxFetchValueUsesNativeTransaction(t *testing.T) {
	native := &fakeNativeTx{
		rows: newFakeRows([]string{"count"}, []any{int64(3)}),
	}

	got, err := wrapTx(native).FetchValue[int64](context.Background(), "select")
	if err != nil {
		t.Fatalf("FetchValue: %v", err)
	}
	if got != 3 {
		t.Fatalf("FetchValue = %d, want 3", got)
	}
}

func TestTxTransactionCommitsNestedTransaction(t *testing.T) {
	nested := &fakeNativeTx{}
	root := wrapTx(&fakeNativeTx{nested: nested})
	var received *Tx

	err := root.Transaction(context.Background(), func(tx *Tx) error {
		received = tx

		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if received == nil || received.Tx != nested {
		t.Fatal("callback did not receive the nested transaction wrapper")
	}
	if nested.commitCalls != 1 || nested.rollbackCalls != 0 {
		t.Fatalf(
			"commit/rollback calls = %d/%d, want 1/0",
			nested.commitCalls,
			nested.rollbackCalls,
		)
	}
}

func TestTxTransactionRollsBackNestedTransaction(t *testing.T) {
	nested := &fakeNativeTx{}
	root := wrapTx(&fakeNativeTx{nested: nested})
	want := errors.New("callback failed")

	err := root.Transaction(context.Background(), func(*Tx) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Transaction error = %v, want %v", err, want)
	}
	if nested.commitCalls != 0 || nested.rollbackCalls != 1 {
		t.Fatalf(
			"commit/rollback calls = %d/%d, want 0/1",
			nested.commitCalls,
			nested.rollbackCalls,
		)
	}
}

func TestTxTransactionRollsBackAndRepanics(t *testing.T) {
	nested := &fakeNativeTx{}
	root := wrapTx(&fakeNativeTx{nested: nested})
	const want = "panic value"

	defer func() {
		if got := recover(); got != want {
			t.Fatalf("panic = %#v, want %#v", got, want)
		}
		if nested.commitCalls != 0 || nested.rollbackCalls != 1 {
			t.Fatalf(
				"commit/rollback calls = %d/%d, want 0/1",
				nested.commitCalls,
				nested.rollbackCalls,
			)
		}
	}()

	_ = root.Transaction(context.Background(), func(*Tx) error { panic(want) })
}

func TestTxBeginNestedWrapsNativeTransaction(t *testing.T) {
	nested := &fakeNativeTx{}
	native := &fakeNativeTx{nested: nested}

	got, err := wrapTx(native).BeginNested(context.Background())
	if err != nil {
		t.Fatalf("BeginNested: %v", err)
	}
	if got.Tx != nested {
		t.Fatal("BeginNested wrapped a different transaction")
	}
	if native.beginCalls != 1 {
		t.Fatalf("Begin calls = %d, want 1", native.beginCalls)
	}
}
