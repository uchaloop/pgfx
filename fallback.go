package pgfx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ReadWithFallback runs read on replica, then once on master only if replica
// fails with a connection, TLS, authentication or server-unavailability error.
// SQL, decoding and ordinary context errors do not cause fallback. A connect
// timeout permits fallback only while ctx remains active. Both attempts share ctx.
//
// read must perform repeatable reads without writes or external side effects.
// Results must be fully consumed before returning; do not return Rows or Tx.
// Wrap an entire paginated read to retry both list and count on the same database.
// No transaction is transferred between databases. Borrowed DBs are not closed.
// Any failure returns zero T. If both attempts fail, the error wraps both causes.
// ctx, replica, master and read must be non-nil; DBs must be initialized before use.
func ReadWithFallback[T any](ctx context.Context, replica, master *DB, read func(context.Context, *DB) (T, error)) (T, error) {
	var zero T
	if replica == nil || master == nil || read == nil {
		return zero, errors.New("database and read callback must be non-nil")
	}

	if err := ctx.Err(); err != nil {
		return zero, err
	}

	value, replicaErr := read(ctx, replica)
	if replicaErr == nil {
		return value, nil
	}

	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !isReadUnavailable(replicaErr, false) {
		return zero, replicaErr
	}

	value, masterErr := read(ctx, master)
	if masterErr == nil {
		return value, nil
	}

	return zero, errors.Join(fmt.Errorf("replica read: %w", replicaErr), fmt.Errorf("master read: %w", masterErr))
}

func isReadUnavailable(err error, isConnect bool) bool {
	if err == nil {
		return false
	}
	switch err.(type) {
	case pgx.ScanArgError, *pgx.ScanArgError:
		return false
	case *healthHookError:
		isConnect = false
	case *pgconn.ConnectError:
		isConnect = true
	}
	if state, isOK := err.(interface{ SQLState() string }); isOK {
		switch state.SQLState() {
		case "08000", "08001", "08003", "08004", "08006", "08007", "08P01",
			"28000", "28P01", "3D000", "53300", "57P01", "57P02", "57P03", "57P04":
			return true
		}
		return false
	}
	if joined, isOK := err.(interface{ Unwrap() []error }); isOK {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isReadUnavailable(child, isConnect) {
				return false
			}
		}
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if err == context.DeadlineExceeded {
		return isConnect
	}
	switch err.(type) {
	case *tls.CertificateVerificationError, tls.RecordHeaderError, tls.AlertError,
		x509.UnknownAuthorityError, x509.HostnameError, x509.CertificateInvalidError, x509.SystemRootsError:
		return true
	}
	if wrapped, isOK := err.(interface{ Unwrap() error }); isOK && wrapped.Unwrap() != nil {
		// Network wrappers establish transport context without making a bare
		// callback deadline retryable.
		_, isNetwork := err.(net.Error)
		return isReadUnavailable(wrapped.Unwrap(), isConnect || isNetwork)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, pgconn.ErrConnClosed) {
		return true
	}
	_, isNetwork := err.(net.Error)
	return isConnect || isNetwork
}
