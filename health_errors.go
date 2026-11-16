package pgfx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultHealthSQLStates returns a fresh copy of the recommended connection,
// authentication and server-unavailability codes. Pass it explicitly to
// WithHealthSQLStates; no SQLSTATE codes are enabled automatically.
func DefaultHealthSQLStates() []string {
	return []string{
		"08000", "08001", "08003", "08004", "08006", "08007", "08P01",
		"28000", "28P01", "3D000", "57P01", "57P02", "57P03", "57P04",
	}
}

// WithHealthSQLStates replaces this connection's health SQLSTATE policy.
// It copies codes immediately and removes duplicates. The last option wins;
// no option or an empty list means no SQLSTATE triggers shutdown. Codes must
// contain exactly five ASCII uppercase letters or digits; construction validates
// the final list even when health checks are disabled. Transport failures without
// SQLSTATE and health-check timeouts are handled independently. This option does
// not enable monitoring or change startup Ping; use Config.Health for monitoring.
func WithHealthSQLStates(codes ...string) Option {
	codes = slices.Clone(codes)
	slices.Sort(codes)
	codes = slices.Compact(codes)

	return func(o *options) {
		o.healthSQLStates = slices.Clone(codes)
	}
}

func validateHealthSQLStates(codes []string) error {
	for _, code := range codes {
		if !isHealthSQLStateValid(code) {
			return errors.New("invalid health SQLSTATE: expected five uppercase letters or digits")
		}
	}

	return nil
}

// Connection errors supply a fallback category for untyped protocol/TLS errors.
// Hook boundaries clear that fallback without hiding typed causes or SQLSTATE.
func classifyHealthError(err error, codes []string) (string, string) {
	return classifyHealthCause(err, codes, "")
}

func classifyHealthCause(err error, codes []string, category string) (string, string) {
	if err == nil {
		return "", ""
	}

	if state, isOK := err.(interface{ SQLState() string }); isOK {
		code := state.SQLState()
		if slices.Contains(codes, code) {
			return "sqlstate", code
		}

		return "", ""
	}

	switch err.(type) {
	case *healthHookError:
		category = ""
	case *pgconn.ConnectError, net.Error:
		category = "connection"
	case *tls.CertificateVerificationError, tls.RecordHeaderError, tls.AlertError,
		x509.UnknownAuthorityError, x509.HostnameError, x509.CertificateInvalidError, x509.SystemRootsError:
		category = "tls"
	}

	if joined, isOK := err.(interface{ Unwrap() []error }); isOK {
		for _, child := range joined.Unwrap() {
			if result, code := classifyHealthCause(child, codes, category); len(result) != 0 {
				return result, code
			}
		}

		return "", ""
	}

	if pgconn.Timeout(err) {
		category = "timeout"
	}

	if wrapped, isOK := err.(interface{ Unwrap() error }); isOK && wrapped.Unwrap() != nil {
		return classifyHealthCause(wrapped.Unwrap(), codes, category)
	}

	if errors.Is(err, context.Canceled) {
		return "", ""
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return "timeout", ""
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, pgconn.ErrConnClosed) {
		return "connection", ""
	}

	return category, ""
}

func isHealthSQLStateValid(code string) bool {
	if len(code) != 5 {
		return false
	}

	for _, c := range code {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}

	return true
}
