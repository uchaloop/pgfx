package pgfx

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsConstraint(t *testing.T) {
	first := &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}
	second := &pgconn.PgError{Code: "23503", ConstraintName: "orders_user_key"}

	for _, err := range []error{first, fmt.Errorf("insert: %w", first), errors.Join(first, second), postgresErrorAdapter{value: first}} {
		if !IsConstraint(err, "23505", "users_email_key") {
			t.Fatal("constraint not matched")
		}

		if IsConstraint(err, "23503", "users_email_key") || IsConstraint(err, "23505", "orders_user_key") || IsConstraint(err, "", "users_email_key") || IsConstraint(err, "23505", "") {
			t.Fatal("unrelated constraint matched")
		}
	}

	if IsConstraint(errors.Join(first, second), "23503", "orders_user_key") {
		t.Fatal("matched second PostgreSQL error")
	}

	var typedNil *pgconn.PgError

	for _, err := range []error{nil, typedNil, errors.New("other")} {
		if IsConstraint(err, "23505", "users_email_key") {
			t.Fatal("non-PostgreSQL error matched")
		}
	}
}

func TestFetchOptionalRow(t *testing.T) {
	type row struct {
		ID int64 `db:"id"`
	}

	for _, item := range []struct {
		name     string
		values   [][]any
		queryErr error
		scanErr  error
		rowsErr  error
		wantErr  error
		isFound  bool
	}{
		{name: "empty"},
		{name: "one", values: [][]any{{int64(7)}}, isFound: true},
		{name: "many", values: [][]any{{int64(7)}, {int64(8)}}, wantErr: pgx.ErrTooManyRows},
		{name: "query", queryErr: context.Canceled, wantErr: context.Canceled},
		{name: "query_no_rows", queryErr: pgx.ErrNoRows, wantErr: pgx.ErrNoRows},
		{
			name:    "scan",
			values:  [][]any{{int64(7)}},
			scanErr: pgx.ErrNoRows,
			wantErr: pgx.ErrNoRows,
		},
		{name: "iteration", rowsErr: context.Canceled, wantErr: context.Canceled},
	} {
		t.Run(item.name, func(t *testing.T) {
			rows := makeFakeRows([]string{"id"}, item.values...)
			rows.scanErr = item.scanErr
			rows.err = item.rowsErr
			tx := makeTx(&fakeNativeTx{rows: rows, queryErr: item.queryErr})

			got, isFound, err := tx.FetchOptionalRow[row](context.Background(), "select")
			if !errors.Is(err, item.wantErr) || isFound != item.isFound {
				t.Fatalf(
					"result: %+v, %v, %v",
					got,
					isFound,
					err,
				)
			}

			if isFound && got.ID != 7 {
				t.Fatalf("row: %+v", got)
			}

			if !isFound && got != (row{}) {
				t.Fatalf("nonzero absent result: %+v", got)
			}

			if item.queryErr == nil && !rows.isClosed {
				t.Fatal("rows not closed")
			}
		})
	}
}

type optionalTerminalRows struct {
	*fakeRows
	terminalErr error
}

func (r *optionalTerminalRows) Err() error {
	if r.isClosed {
		return r.terminalErr
	}

	return nil
}

func TestFetchOptionalRowTerminalError(t *testing.T) {
	type row struct {
		ID int64 `db:"id"`
	}

	rows := &optionalTerminalRows{
		fakeRows:    makeFakeRows([]string{"id"}, []any{int64(7)}),
		terminalErr: context.Canceled,
	}
	tx := makeTx(&fakeNativeTx{rows: rows})

	got, isFound, err := tx.FetchOptionalRow[row](context.Background(), "select")
	if got != (row{}) || isFound || !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"terminal error: %+v, %v, %v",
			got,
			isFound,
			err,
		)
	}

	if !rows.isClosed {
		t.Fatal("rows not closed")
	}
}
