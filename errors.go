package pgfx

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// AsPgError returns the first *pgconn.PgError found by errors.AsType, or nil.
// Wrapping and errors.Join are supported; joined errors follow errors.AsType order.
// The returned pointer is borrowed from the original error, not copied.
func AsPgError(err error) *pgconn.PgError {
	pgErr, _ := errors.AsType[*pgconn.PgError](err)

	return pgErr
}

// SQLState returns the first PostgreSQL error's SQLSTATE, or an empty string.
// It does not parse error messages or replace the original error.
func SQLState(err error) string {
	if pgErr := AsPgError(err); pgErr != nil {
		return pgErr.SQLState()
	}

	return ""
}

// IsSQLState reports whether the first PostgreSQL error has the given nonempty code.
// Classification does not imply that retrying an operation is safe.
func IsSQLState(err error, code string) bool {
	return len(code) != 0 && SQLState(err) == code
}

// IsUniqueViolation reports a uniqueness constraint violation (SQLSTATE 23505).
func IsUniqueViolation(err error) bool {
	return IsSQLState(err, "23505")
}

// IsForeignKeyViolation reports a foreign key violation (SQLSTATE 23503).
func IsForeignKeyViolation(err error) bool {
	return IsSQLState(err, "23503")
}

// IsNotNullViolation reports a NOT NULL violation (SQLSTATE 23502).
func IsNotNullViolation(err error) bool {
	return IsSQLState(err, "23502")
}

// IsCheckViolation reports a CHECK violation (SQLSTATE 23514).
func IsCheckViolation(err error) bool {
	return IsSQLState(err, "23514")
}

// IsExclusionViolation reports an exclusion constraint violation (SQLSTATE 23P01).
func IsExclusionViolation(err error) bool {
	return IsSQLState(err, "23P01")
}

// IsInvalidTextRepresentation reports invalid text input for a PostgreSQL type (SQLSTATE 22P02).
func IsInvalidTextRepresentation(err error) bool {
	return IsSQLState(err, "22P02")
}

// IsNumericValueOutOfRange reports a numeric value outside its allowed range (SQLSTATE 22003).
func IsNumericValueOutOfRange(err error) bool {
	return IsSQLState(err, "22003")
}

// IsSerializationFailure reports a serialization failure (SQLSTATE 40001).
// It does not retry; any retry policy must consider the entire transaction.
func IsSerializationFailure(err error) bool {
	return IsSQLState(err, "40001")
}

// IsDeadlockDetected reports a detected deadlock (SQLSTATE 40P01).
// It does not retry; any retry policy must consider the entire transaction.
func IsDeadlockDetected(err error) bool {
	return IsSQLState(err, "40P01")
}

// IsUndefinedTable reports an undefined table (SQLSTATE 42P01).
func IsUndefinedTable(err error) bool {
	return IsSQLState(err, "42P01")
}

// IsUndefinedColumn reports an undefined column (SQLSTATE 42703).
func IsUndefinedColumn(err error) bool {
	return IsSQLState(err, "42703")
}

// IsConstraint reports whether the first PostgreSQL error matches both code
// and name exactly. Both must be nonempty. Constraint names are not globally
// unique; use this only when the query's table/schema is known, or inspect
// AsPgError for that additional context. It does not establish retry safety.
func IsConstraint(err error, code, name string) bool {
	if len(code) == 0 || len(name) == 0 {
		return false
	}

	pgErr := AsPgError(err)

	return pgErr != nil && pgErr.Code == code && pgErr.ConstraintName == name
}
