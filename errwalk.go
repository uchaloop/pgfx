package pgfx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// leafKind is what an error leaf means for the database behind it.
type leafKind uint8

const (
	leafOther leafKind = iota
	leafDecode
	leafServer
	leafCanceled
	leafDeadline
	leafTLS
	leafClosed
	leafNetwork
	leafBreakerOpen
)

// errorLeaf is one cause the walk does not unwrap further, together with the
// transport context of the wrappers above it.
type errorLeaf struct {
	kind      leafKind
	code      string
	isConnect bool
}

// unavailableStates are the SQLSTATE codes that mean the server or the
// connection is unavailable rather than the statement being wrong.
var unavailableStates = map[string]struct{}{
	"08000": {}, "08001": {}, "08003": {}, "08004": {}, "08006": {}, "08007": {}, "08P01": {},
	"28000": {}, "28P01": {}, "3D000": {}, "53300": {},
	"57P01": {}, "57P02": {}, "57P03": {}, "57P04": {},
}

// walkError visits the leaves of err. A decoding error (pgx.ScanArgError), an
// error with a SQLSTATE, a TLS error, a cancellation and a bare deadline are
// leaves; joined errors visit every part; single wrappers pass the "connect"
// context down, and a net.Error wrapper turns it on. Policies decide what the
// leaves mean, so the walk itself has one definition of the error boundaries.
func walkError(err error, isConnect bool, visit func(errorLeaf)) {
	if err == nil {
		visit(errorLeaf{kind: leafOther})

		return
	}

	switch err.(type) {
	case pgx.ScanArgError, *pgx.ScanArgError:
		visit(errorLeaf{kind: leafDecode})

		return
	case *healthHookError:
		isConnect = false
	case *pgconn.ConnectError:
		isConnect = true
	}

	if state, isOK := err.(interface{ SQLState() string }); isOK {
		visit(errorLeaf{kind: leafServer, code: state.SQLState(), isConnect: isConnect})

		return
	}

	if joined, isOK := err.(interface{ Unwrap() []error }); isOK {
		children := joined.Unwrap()
		if len(children) == 0 {
			visit(errorLeaf{kind: leafOther, isConnect: isConnect})

			return
		}

		for _, child := range children {
			walkError(child, isConnect, visit)
		}

		return
	}

	if err == context.DeadlineExceeded {
		visit(errorLeaf{kind: leafDeadline, isConnect: isConnect})

		return
	}

	switch err.(type) {
	case *tls.CertificateVerificationError, tls.RecordHeaderError, tls.AlertError,
		x509.UnknownAuthorityError, x509.HostnameError, x509.CertificateInvalidError, x509.SystemRootsError:
		visit(errorLeaf{kind: leafTLS})

		return
	}

	if wrapped, isOK := err.(interface{ Unwrap() error }); isOK && wrapped.Unwrap() != nil {
		// Network wrappers establish transport context without making a bare
		// callback deadline retryable.
		_, isNetwork := err.(net.Error)
		walkError(wrapped.Unwrap(), isConnect || isNetwork, visit)

		return
	}

	// Cancellation is detected only at leaves: checking errors.Is on a wrapper
	// would look inside decoding errors and other parts the walk keeps apart.
	if errors.Is(err, context.Canceled) {
		visit(errorLeaf{kind: leafCanceled})

		return
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, pgconn.ErrConnClosed) {
		visit(errorLeaf{kind: leafClosed})

		return
	}

	if errors.Is(err, ErrBreakerOpen) {
		visit(errorLeaf{kind: leafBreakerOpen})

		return
	}

	if _, isNetwork := err.(net.Error); isNetwork {
		visit(errorLeaf{kind: leafNetwork})

		return
	}

	visit(errorLeaf{kind: leafOther, isConnect: isConnect})
}

// isLeafUnavailable is the fallback reading of one leaf: the database could
// not be reached, so another database may serve the same read.
func isLeafUnavailable(leaf errorLeaf) bool {
	switch leaf.kind {
	case leafServer:
		_, isUnavailable := unavailableStates[leaf.code]

		return isUnavailable
	case leafDeadline, leafOther:
		return leaf.isConnect
	case leafTLS, leafClosed, leafNetwork, leafBreakerOpen:
		return true
	default:
		return false
	}
}

// isReadUnavailable is the fallback policy: err means unavailability only when
// every leaf does. isConnect marks an error that already happened while
// connecting.
func isReadUnavailable(err error, isConnect bool) bool {
	if err == nil {
		return false
	}

	isUnavailable := true

	walkError(err, isConnect, func(leaf errorLeaf) {
		isUnavailable = isUnavailable && isLeafUnavailable(leaf)
	})

	return isUnavailable
}
