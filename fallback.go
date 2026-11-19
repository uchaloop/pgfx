package pgfx

import (
	"context"
	"errors"
	"fmt"
)

// ErrBreakerOpen reports a connection whose breaker refuses new operations.
// IsUnavailable treats it as unavailability, so ReadWithFallback moves past
// such a database.
var ErrBreakerOpen = errors.New("connection breaker is open")

// IsUnavailable reports whether err means the database could not be reached:
// a connection, TLS or authentication failure, a server-unavailability
// SQLSTATE (08xxx, 28000, 28P01, 3D000, 53300, 57P01-57P04), a closed
// connection, or ErrBreakerOpen. Cancellation, statement errors, decoding
// errors (pgx.ScanArgError) and timeouts are not unavailability; the one
// exception is a deadline reached while connecting. A joined error is
// unavailable only when every part is.
func IsUnavailable(err error) bool {
	return isReadUnavailable(err, false)
}

// ReadWithFallback runs read on dbs in order and moves to the next database
// only when the previous attempt fails with IsUnavailable. The order is the
// priority: pass []*DB{master, replica} to read the master first. SQL, decoding
// and ordinary context errors are returned unchanged without fallback. A
// connect timeout permits fallback only while ctx remains active. All attempts
// share ctx.
//
// read must perform repeatable reads without writes or external side effects.
// Results must be fully consumed before returning; do not return Rows or Tx.
// Wrap an entire paginated read to retry it on the same database. No
// transaction is transferred between databases. Borrowed DBs are not closed.
//
// Any failure returns zero T. When more than one attempt was made and the last
// one failed, the error joins every attempt's cause, each labelled "read
// <name>" with the connection name, with its position when names repeat, or
// "read database <i> of <n>" when unnamed. dbs must be non-empty and contain no
// nil; ctx and read must be non-nil.
func ReadWithFallback[T any](ctx context.Context, dbs []*DB, read func(context.Context, *DB) (T, error)) (T, error) {
	var zero T

	if len(dbs) == 0 || read == nil {
		return zero, errors.New("databases and read callback must be non-empty")
	}

	for _, db := range dbs {
		if db == nil {
			return zero, errors.New("databases must be non-nil")
		}
	}

	if err := ctx.Err(); err != nil {
		return zero, err
	}

	causes := make([]error, 0, len(dbs))

	for _, db := range dbs {
		value, err := read(ctx, db)
		if err == nil {
			return value, nil
		}

		causes = append(causes, err)

		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}

		if !isReadUnavailable(err, false) {
			break
		}
	}

	if len(causes) == 1 {
		return zero, causes[0]
	}

	return zero, labelCauses(dbs, causes)
}

// labelCauses joins the causes of the attempted databases, labelling each one.
func labelCauses(dbs []*DB, causes []error) error {
	counts := make(map[string]int, len(dbs))

	for _, db := range dbs {
		counts[db.Name()]++
	}

	labelled := make([]error, len(causes))

	for i, cause := range causes {
		name := dbs[i].Name()

		switch {
		case len(name) == 0:
			labelled[i] = fmt.Errorf("read database %d of %d: %w", i+1, len(dbs), cause)
		case counts[name] > 1:
			labelled[i] = fmt.Errorf("read %s (%d of %d): %w", name, i+1, len(dbs), cause)
		default:
			labelled[i] = fmt.Errorf("read %s: %w", name, cause)
		}
	}

	return errors.Join(labelled...)
}
