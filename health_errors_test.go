package pgfx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"go.uber.org/fx"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestHealthErrorPolicy(t *testing.T) {
	selected := &pgconn.PgError{Code: "57P03"}
	ignored := &pgconn.PgError{Code: "53300"}

	for _, tc := range []struct {
		name    string
		err     error
		codes   []string
		isMatch bool
	}{
		{name: "success"},
		{name: "no implicit defaults", err: selected},
		{name: "selected", err: selected, codes: DefaultHealthSQLStates(), isMatch: true},
		{name: "unselected", err: ignored, codes: DefaultHealthSQLStates()},
		{name: "wrapped", err: fmt.Errorf("wrapped: %w", selected), codes: DefaultHealthSQLStates(), isMatch: true},
		{name: "joined transport", err: errors.Join(ignored, io.EOF), isMatch: true},
		{name: "joined codes", err: errors.Join(ignored, selected), codes: DefaultHealthSQLStates(), isMatch: true},
		{name: "network", err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("secret")}, isMatch: true},
		{name: "timeout", err: context.DeadlineExceeded, isMatch: true},
		{name: "canceled", err: context.Canceled},
		{name: "closed", err: pgconn.ErrConnClosed, isMatch: true},
		{name: "wrapped code beats transport", err: &net.OpError{Op: "dial", Err: ignored}, codes: DefaultHealthSQLStates()},
		{name: "tls", err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, isMatch: true},
		{name: "unknown hook", err: errors.New("hook failed")},
		{name: "wrapped joined cancellation", err: fmt.Errorf("wrapped: %w", errors.Join(context.Canceled, io.EOF)), isMatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			category, _ := classifyHealthError(tc.err, tc.codes)
			if (len(category) != 0) != tc.isMatch {
				t.Fatalf("category = %q, want match %v", category, tc.isMatch)
			}
		})
	}
}

func TestHealthSQLStatesOptions(t *testing.T) {
	a := DefaultHealthSQLStates()
	a[0] = "XXXXX"
	if DefaultHealthSQLStates()[0] != "08000" {
		t.Fatal("shared defaults")
	}

	codes := []string{"53300", "53300"}
	option := WithHealthSQLStates(codes...)
	codes[0] = "XXXXX"
	opts := makeOptions(option)
	if len(opts.healthSQLStates) != 1 || opts.healthSQLStates[0] != "53300" {
		t.Fatal("option did not copy and deduplicate")
	}

	if len(makeOptions(option, WithHealthSQLStates()).healthSQLStates) != 0 {
		t.Fatal("empty option did not replace")
	}

	for _, code := range []string{"", "P01", "57p03", " 57P03", "57P03 ", "57*03"} {
		if _, err := makeValidConfig().poolConfig(makeOptions(WithHealthSQLStates(code))); err == nil {
			t.Fatalf("accepted %q", code)
		}
	}

	if _, err := makeValidConfig().poolConfig(makeOptions(WithHealthSQLStates("bad"), WithHealthSQLStates("53300"))); err != nil {
		t.Fatal(err)
	}
}

func TestHealthSQLStatesValidateBeforeConnect(t *testing.T) {
	cfg := makeValidConfig()
	cfg.Health.IsEnabled = false
	if db, err := MakeDB(context.Background(), cfg, WithHealthSQLStates("bad")); err == nil {
		db.Close()
		t.Fatal("MakeDB accepted invalid SQLSTATE")
	}

	var connection *Connection
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		MakeModule(WithHealthSQLStates("bad")),
		fx.Populate(&connection),
	)
	if app.Err() == nil {
		t.Fatal("Fx accepted invalid SQLSTATE")
	}
}

func TestHealthSQLStatesReusableOption(t *testing.T) {
	option := WithHealthSQLStates("53300")
	first := makeOptions(option)
	second := makeOptions(option)
	first.healthSQLStates[0] = "57P03"
	if second.healthSQLStates[0] != "53300" || makeOptions(option).healthSQLStates[0] != "53300" {
		t.Fatal("shared mutable policy")
	}
}

func TestHealthHookErrorContract(t *testing.T) {
	errOriginal := errors.New("hook failed")
	wrapped := wrapHealthHookError(errOriginal)
	if !errors.Is(wrapped, errOriginal) || wrapped.Error() != errOriginal.Error() {
		t.Fatal("hook error contract changed")
	}
	if wrapHealthHookError(nil) != nil {
		t.Fatal("nil error was wrapped")
	}
	preferred := &pgconn.NotPreferredError{}
	if wrapHealthHookError(preferred) != preferred {
		t.Fatal("prefer-standby signal was wrapped")
	}
}
