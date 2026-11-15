package pgfx

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresErrorClassification(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "email_key"}
	wrapped := fmt.Errorf("insert: %w", pgErr)
	joined := errors.Join(errors.New("cleanup"), wrapped)

	for _, err := range []error{pgErr, wrapped, joined} {
		if AsPgError(err) != pgErr || SQLState(err) != "23505" || !IsSQLState(err, "23505") {
			t.Fatalf("classification lost PostgreSQL error: %v", err)
		}

		if IsSQLState(err, "") || IsSQLState(err, "23503") {
			t.Fatal("matched wrong code")
		}
	}

	var typedNil *pgconn.PgError

	for _, err := range []error{nil, errors.New("other"), typedNil} {
		if AsPgError(err) != nil || len(SQLState(err)) != 0 || IsSQLState(err, "") || IsSQLState(err, "23505") {
			t.Fatal("non-PostgreSQL error classified")
		}
	}
}

func TestPostgresErrorPredicates(t *testing.T) {
	for _, item := range []struct {
		code    string
		isMatch func(error) bool
	}{
		{code: "23505", isMatch: IsUniqueViolation},
		{code: "23503", isMatch: IsForeignKeyViolation},
		{code: "23502", isMatch: IsNotNullViolation},
		{code: "23514", isMatch: IsCheckViolation},
		{code: "23P01", isMatch: IsExclusionViolation},
		{code: "22P02", isMatch: IsInvalidTextRepresentation},
		{code: "22003", isMatch: IsNumericValueOutOfRange},
		{code: "40001", isMatch: IsSerializationFailure},
		{code: "40P01", isMatch: IsDeadlockDetected},
		{code: "42P01", isMatch: IsUndefinedTable},
		{code: "42703", isMatch: IsUndefinedColumn},
	} {
		t.Run(item.code, func(t *testing.T) {
			if !item.isMatch(fmt.Errorf("query: %w", &pgconn.PgError{Code: item.code})) {
				t.Fatal("wrapped error not matched")
			}

			if item.isMatch(nil) || item.isMatch(&pgconn.PgError{Code: "XX000"}) {
				t.Fatal("unrelated error matched")
			}
		})
	}
}
