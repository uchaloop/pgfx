package pgfx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestHealthConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		health  HealthConfig
		isValid bool
	}{
		{name: "disabled", isValid: true},
		{name: "enabled_zero", health: HealthConfig{IsEnabled: true}},
		{name: "valid", health: HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      time.Second,
			FailureAfter: time.Second,
		}, isValid: true},
		{name: "negative_interval", health: HealthConfig{
			IsEnabled:    true,
			Interval:     -time.Second,
			Timeout:      time.Second,
			FailureAfter: time.Second,
		}},
		{name: "negative_timeout", health: HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      -time.Second,
			FailureAfter: time.Second,
		}},
		{name: "long_timeout", health: HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      2 * time.Second,
			FailureAfter: time.Second,
		}},
		{name: "short_window", health: HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      time.Second,
			FailureAfter: time.Millisecond,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := makeValidConfig()
			cfg.Health = tc.health
			if err := cfg.Validate(); (err == nil) != tc.isValid {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestHealthMonitorRecoveryAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := HealthConfig{
			IsEnabled:    true,
			Interval:     10 * time.Second,
			Timeout:      3 * time.Second,
			FailureAfter: 30 * time.Second,
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var logs bytes.Buffer
		var calls, shutdowns atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)

			runHealthMonitor(
				ctx,
				cfg,
				func(context.Context) error {
					calls.Add(1)
					if calls.Load() == 3 {
						return nil
					}

					return errors.New("password=secret-value")
				},
				func() error {
					shutdowns.Add(1)

					return nil
				},
				slog.New(slog.NewTextHandler(&logs, nil)),
			)
		}()
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("checked before first interval")
		}

		time.Sleep(60 * time.Second)
		synctest.Wait()
		if shutdowns.Load() != 0 {
			t.Fatal("success did not reset failure window")
		}

		time.Sleep(10 * time.Second)
		synctest.Wait()
		<-done
		if calls.Load() != 7 || shutdowns.Load() != 1 {
			t.Fatalf("calls=%d shutdowns=%d", calls.Load(), shutdowns.Load())
		}

		time.Sleep(time.Minute)
		synctest.Wait()
		if shutdowns.Load() != 1 {
			t.Fatal("repeated shutdown")
		}

		if strings.Contains(logs.String(), "secret-value") {
			t.Fatal("error leaked")
		}

		for _, message := range []string{"database health check failed", "database health restored", "database health shutdown requested"} {
			if !strings.Contains(logs.String(), message) {
				t.Fatalf("missing diagnostic %q", message)
			}
		}
	})
}

func TestHealthMonitorCancellationDuringCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      time.Second,
			FailureAfter: time.Second,
		}
		ctx, cancel := context.WithCancel(context.Background())
		var logs bytes.Buffer
		var calls, shutdowns atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)

			runHealthMonitor(
				ctx,
				cfg,
				func(ctx context.Context) error {
					calls.Add(1)
					<-ctx.Done()

					return ctx.Err()
				},
				func() error {
					shutdowns.Add(1)

					return nil
				},
				slog.New(slog.NewTextHandler(&logs, nil)),
			)
		}()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		cancel()
		synctest.Wait()
		<-done
		if calls.Load() != 1 || shutdowns.Load() != 0 || logs.Len() != 0 {
			t.Fatalf(
				"calls=%d shutdowns=%d logs=%s",
				calls.Load(),
				shutdowns.Load(),
				&logs,
			)
		}
	})
}

func TestHealthMonitorTimeoutAndShutdownError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := HealthConfig{
			IsEnabled:    true,
			Interval:     time.Second,
			Timeout:      time.Second,
			FailureAfter: time.Second,
		}
		var logs bytes.Buffer
		var calls, shutdowns atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)

			runHealthMonitor(
				context.Background(),
				cfg,
				func(ctx context.Context) error {
					calls.Add(1)
					<-ctx.Done()

					return ctx.Err()
				},
				func() error {
					shutdowns.Add(1)

					return errors.New("sensitive shutdown detail")
				},
				slog.New(slog.NewTextHandler(&logs, nil)),
			)
		}()
		<-done
		if calls.Load() != 2 || shutdowns.Load() != 1 {
			t.Fatalf("calls=%d shutdowns=%d", calls.Load(), shutdowns.Load())
		}

		if !strings.Contains(logs.String(), "database health shutdown request failed") || strings.Contains(logs.String(), "sensitive") {
			t.Fatalf("unsafe diagnostic: %s", &logs)
		}
	})
}
