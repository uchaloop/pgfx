package pgfx

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

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
	want := errors.New("operation failed")

	for _, item := range []struct {
		name        string
		beginErr    error
		callbackErr error
		commitErr   error
		rollbackErr error
		wantErr     error
	}{
		{name: "success"},
		{name: "begin", beginErr: want, wantErr: want},
		{name: "callback", callbackErr: want, wantErr: want},
		{name: "commit", commitErr: want, wantErr: want},
		{
			name:        "rollback",
			callbackErr: want,
			rollbackErr: errors.New("rollback failed"),
			wantErr:     want,
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

type postgresErrorAdapter struct{ value *pgconn.PgError }

func (e postgresErrorAdapter) Error() string { return "adapted error" }

func (e postgresErrorAdapter) As(target any) bool {
	p, isOK := target.(**pgconn.PgError)
	if isOK {
		*p = e.value
	}

	return isOK
}

func TestAsPgErrorCustomAndJoinOrder(t *testing.T) {
	first := &pgconn.PgError{Code: "23505"}
	second := &pgconn.PgError{Code: "23503"}

	for _, err := range []error{postgresErrorAdapter{value: first}, errors.Join(first, second)} {
		if AsPgError(err) != first {
			t.Fatal("first matching error lost")
		}
	}
}
