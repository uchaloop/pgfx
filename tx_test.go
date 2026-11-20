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
	isClosed      bool
}

func (tx *fakeNativeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return tx.rows, tx.queryErr
}

func (tx *fakeNativeTx) Begin(context.Context) (pgx.Tx, error) {
	tx.beginCalls++

	return tx.nested, tx.beginErr
}

func (tx *fakeNativeTx) Commit(context.Context) error {
	if tx.isClosed {
		return pgx.ErrTxClosed
	}

	tx.isClosed = true
	tx.commitCalls++

	return nil
}

func (tx *fakeNativeTx) Rollback(context.Context) error {
	if tx.isClosed {
		return pgx.ErrTxClosed
	}

	tx.isClosed = true
	tx.rollbackCalls++

	return nil
}

func TestFetchIntoAppendsAndReuses(t *testing.T) {
	data := makeWarehouseRows(2, 3)
	tx := makeTx(&fakeNativeTx{rows: data})
	dst := make([]warehouse, 1, 3)
	dst[0].ID = 1
	result, err := tx.FetchRowsInto(context.Background(), dst, "query")
	if err != nil || len(result) != 3 || result[0].ID != 1 || result[2].ID != 3 || &result[0] != &dst[0] || !data.isClosed {
		t.Fatalf(
			"result=%v closed=%v err=%v",
			result,
			data.isClosed,
			err,
		)
	}

	values := makeFakeRows([]string{"id"}, []any{int64(7)}, []any{int64(8)})
	tx = makeTx(&fakeNativeTx{rows: values})
	buffer := make([]int64, 3)
	got, err := tx.FetchValuesInto(context.Background(), buffer[:0], "query")
	if err != nil || len(got) != 2 || got[0] != 7 || got[1] != 8 || &got[0] != &buffer[0] {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestTxFetchValueUsesNativeTransaction(t *testing.T) {
	native := &fakeNativeTx{
		rows: makeFakeRows([]string{"count"}, []any{int64(3)}),
	}

	got, err := makeTx(native).FetchValue[int64](context.Background(), "select")
	if err != nil {
		t.Fatalf("FetchValue: %v", err)
	}

	if got != 3 {
		t.Fatalf("FetchValue = %d, want 3", got)
	}
}

func TestTxTransactionCommitsNestedTransaction(t *testing.T) {
	nested := &fakeNativeTx{}
	root := makeTx(&fakeNativeTx{nested: nested})
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
	root := makeTx(&fakeNativeTx{nested: nested})
	errExpected := errors.New("callback failed")

	err := root.Transaction(context.Background(), func(*Tx) error { return errExpected })
	if !errors.Is(err, errExpected) {
		t.Fatalf("Transaction error = %v, want %v", err, errExpected)
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
	root := makeTx(&fakeNativeTx{nested: nested})
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

	got, err := makeTx(native).BeginNested(context.Background())
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

type valueNativeTx struct {
	fakeNativeTx
	commitErr   error
	rollbackErr error
}

func (tx *valueNativeTx) Commit(ctx context.Context) error {
	_ = tx.fakeNativeTx.Commit(ctx)

	return tx.commitErr
}

func (tx *valueNativeTx) Rollback(ctx context.Context) error {
	err := tx.fakeNativeTx.Rollback(ctx)
	if err != nil {
		return err
	}

	return tx.rollbackErr
}

func TestTransactionValue(t *testing.T) {
	errExpected := errors.New("operation failed")

	for _, item := range []struct {
		name        string
		beginErr    error
		callbackErr error
		commitErr   error
		rollbackErr error
		wantErr     error
	}{
		{name: "success"},
		{name: "begin", beginErr: errExpected, wantErr: errExpected},
		{name: "callback", callbackErr: errExpected, wantErr: errExpected},
		{name: "commit", commitErr: errExpected, wantErr: errExpected},
		{
			name:        "rollback",
			callbackErr: errExpected,
			rollbackErr: errors.New("rollback failed"),
			wantErr:     errExpected,
		},
	} {
		t.Run(item.name, func(t *testing.T) {
			nested := &valueNativeTx{commitErr: item.commitErr, rollbackErr: item.rollbackErr}
			root := makeTx(&fakeNativeTx{nested: nested, beginErr: item.beginErr})
			isCalled := false

			got, err := root.TransactionValue(context.Background(), func(tx *Tx) (*int, error) {
				isCalled = true
				if tx.Tx != nested {
					t.Fatal("wrong transaction")
				}

				value := 7

				return &value, item.callbackErr
			})
			if !errors.Is(err, item.wantErr) {
				t.Fatalf("error: %v, want %v", err, item.wantErr)
			}

			if item.wantErr != nil && got != nil {
				t.Fatal("result escaped failed transaction")
			}

			if item.wantErr == nil && (got == nil || *got != 7) {
				t.Fatalf("result: %v", got)
			}

			if isCalled != (item.beginErr == nil) {
				t.Fatal("unexpected callback execution")
			}
		})
	}
}

func TestTransactionValuePanic(t *testing.T) {
	nested := &fakeNativeTx{}
	root := makeTx(&fakeNativeTx{nested: nested})
	defer func() {
		if recover() != "failure" || nested.rollbackCalls != 1 {
			t.Fatal("panic or rollback lost")
		}
	}()

	_, _ = root.TransactionValue(context.Background(), func(*Tx) (int, error) { panic("failure") })
}
