package pgfx

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReadWithFallbackErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		isFallback bool
	}{
		{name: "TLS", err: x509.UnknownAuthorityError{}, isFallback: true},
		{name: "closed", err: pgconn.ErrConnClosed, isFallback: true},
		{name: "network", err: &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}, isFallback: true},
		{name: "wrapped EOF", err: fmt.Errorf("read: %w", io.EOF), isFallback: true},
		{name: "unavailable", err: &pgconn.PgError{Code: "57P03"}, isFallback: true},
		{name: "too many connections", err: &pgconn.PgError{Code: "53300"}, isFallback: true},
		{name: "authentication", err: &pgconn.PgError{Code: "28P01"}, isFallback: true},
		{name: "no rows", err: pgx.ErrNoRows},
		{name: "constraint", err: &pgconn.PgError{Code: "23505"}},
		{name: "syntax", err: &pgconn.PgError{Code: "42601"}},
		{name: "statement timeout", err: &pgconn.PgError{Code: "57014"}},
		{name: "serialization", err: &pgconn.PgError{Code: "40001"}},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled},
		{name: "decoding EOF", err: pgx.ScanArgError{Err: io.EOF}},
		{name: "mixed join", err: errors.Join(io.EOF, pgx.ErrNoRows)},
		{name: "hook", err: &healthHookError{err: errors.New("hook failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replica, master := &DB{}, &DB{}
			calls := 0
			value, err := ReadWithFallback(
				t.Context(),
				replica,
				master,
				func(ctx context.Context, db *DB) (int, error) {
					calls++
					if calls == 1 {
						if db != replica {
							t.Fatal("wrong first database")
						}
						return 99, tc.err
					}
					if db != master {
						t.Fatal("wrong fallback database")
					}
					return 7, nil
				},
			)
			if tc.isFallback {
				if calls != 2 || err != nil || value != 7 {
					t.Fatalf(
						"calls=%d value=%d error=%v",
						calls,
						value,
						err,
					)
				}
				return
			}
			if calls != 1 || value != 0 || !errors.Is(err, tc.err) {
				t.Fatalf(
					"calls=%d value=%d error=%v",
					calls,
					value,
					err,
				)
			}
		})
	}
}

func TestReadWithFallbackSuccessAndBothErrors(t *testing.T) {
	replica, master := &DB{}, &DB{}
	calls := 0
	value, err := ReadWithFallback(
		t.Context(),
		replica,
		master,
		func(context.Context, *DB) (int, error) { calls++; return 42, nil },
	)
	if err != nil || value != 42 || calls != 1 {
		t.Fatalf(
			"%d %d %v",
			calls,
			value,
			err,
		)
	}
	first, second := io.EOF, errors.New("master failed")
	calls = 0
	value, err = ReadWithFallback(
		t.Context(),
		replica,
		master,
		func(context.Context, *DB) (int, error) {
			calls++
			if calls == 1 {
				return 99, first
			}
			return 88, second
		},
	)
	if calls != 2 || value != 0 || !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf(
			"%d %d %v",
			calls,
			value,
			err,
		)
	}
}

func TestReadWithFallbackCancellation(t *testing.T) {
	for _, isBefore := range []bool{true, false} {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		if isBefore {
			cancel()
		}
		_, err := ReadWithFallback(
			ctx,
			&DB{},
			&DB{},
			func(context.Context, *DB) (int, error) { calls++; cancel(); return 0, io.EOF },
		)
		cancel()
		want := 1
		if isBefore {
			want = 0
		}
		if calls != want || !errors.Is(err, context.Canceled) {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	}
}

func TestReadWithFallbackConnectTimeout(t *testing.T) {
	cfg, err := pgconn.ParseConfig("host=127.0.0.1 sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }
	_, connectErr := pgconn.ConnectConfig(t.Context(), cfg)
	if !errors.Is(connectErr, context.DeadlineExceeded) {
		t.Fatalf("expected connect deadline: %v", connectErr)
	}
	calls := 0
	_, err = ReadWithFallback(
		t.Context(),
		&DB{},
		&DB{},
		func(context.Context, *DB) (int, error) {
			calls++
			if calls == 1 {
				return 0, fmt.Errorf("read: %w", connectErr)
			}
			return 1, nil
		},
	)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestReadWithFallbackInvalidArguments(t *testing.T) {
	read := func(context.Context, *DB) (int, error) { t.Fatal("callback called"); return 0, nil }
	for _, tc := range []struct {
		replica, master *DB
		read            func(context.Context, *DB) (int, error)
	}{
		{replica: nil, master: &DB{}, read: read},
		{replica: &DB{}, master: nil, read: read},
		{replica: &DB{}, master: &DB{}, read: nil},
	} {
		if _, err := ReadWithFallback(
			t.Context(),
			tc.replica,
			tc.master,
			tc.read,
		); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
}

func TestReadUnavailableDeadlineWrappers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		isConnect  bool
		isExpected bool
	}{
		{
			name:       "connect deadline",
			err:        fmt.Errorf("dial: %w", context.DeadlineExceeded),
			isConnect:  true,
			isExpected: true,
		},
		{name: "callback deadline", err: fmt.Errorf("read: %w", context.DeadlineExceeded)},
		{name: "hook deadline", err: &healthHookError{err: context.DeadlineExceeded}, isConnect: true},
		{name: "mixed connect errors", err: errors.Join(context.DeadlineExceeded, &pgconn.PgError{Code: "42601"}), isConnect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadUnavailable(tc.err, tc.isConnect); got != tc.isExpected {
				t.Fatalf("got %t, want %t", got, tc.isExpected)
			}
		})
	}
}

func TestReadWithFallbackConnectionLimit(t *testing.T) {
	cfg, err := pgconn.ParseConfig("host=127.0.0.1 sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	cause := &pgconn.PgError{Code: "53300", Message: "too many connections"}
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		return nil, cause
	}
	_, err = pgconn.ConnectConfig(t.Context(), cfg)
	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) || !errors.Is(err, cause) {
		t.Fatalf("expected connection error wrapping the limit error: %v", err)
	}

	replica, master := &DB{}, &DB{}
	calls := 0
	value, err := ReadWithFallback(
		t.Context(),
		replica,
		master,
		func(ctx context.Context, db *DB) (int, error) {
			calls++
			if db == replica {
				return 99, fmt.Errorf("read: %w", connectErr)
			}

			if db != master {
				t.Fatal("wrong fallback database")
			}

			return 42, nil
		},
	)
	if err != nil || calls != 2 || value != 42 {
		t.Fatalf(
			"calls=%d value=%d error=%v",
			calls,
			value,
			err,
		)
	}
}
