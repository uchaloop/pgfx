package pgfx_test

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uchaloop/pgfx"
)

func ExampleSQLState() {
	err := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505", ConstraintName: "email_key"})
	fmt.Println(pgfx.SQLState(err), pgfx.IsUniqueViolation(err))
	fmt.Println(pgfx.AsPgError(err).ConstraintName)
	// Output:
	// 23505 true
	// email_key
}

func ExampleEscapeLike() {
	fmt.Println("%" + pgfx.EscapeLike("50%_off") + "%")
	// Output: %50\%\_off%
}

func ExampleIsConstraint() {
	err := fmt.Errorf("insert user: %w", &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"})
	fmt.Println(pgfx.IsConstraint(err, "23505", "users_email_key"))
	fmt.Println(pgfx.IsConstraint(err, "23503", "users_email_key"))
	// Output:
	// true
	// false
}
