package pgfx

import (
	"context"
	"log/slog"
	"time"
)

// HealthConfig controls optional Fx shutdown on sustained pool unavailability.
// It is disabled by default. MakeDB validates these settings but never starts
// a monitor. Enabled Fx checks require HealthModule at the application root,
// and all durations must be explicitly positive, with Timeout <= Interval <= FailureAfter.
//
// Checks run sequentially after the startup Ping, with Interval before the first
// check and between completed checks. Timeout includes pool acquisition. The
// first completed significant failure starts the failure window. Success or an
// error outside the policy resets it; an ignored error is not a recovery event.
// A subsequent significant failure at or beyond FailureAfter requests Fx shutdown once
// per monitor with exit code 1. The monitor then returns, even if that request
// fails. Detection is sampled rather than an exact wall-clock deadline.
//
// SQLSTATE errors count only when selected by WithHealthSQLStates. Pass
// DefaultHealthSQLStates() explicitly for the recommended codes. Transport and
// TLS failures without SQLSTATE, closed connections and check timeouts count
// independently. Unknown errors do not count. Startup Ping is not filtered.
// Pool saturation also counts as unavailability. This policy does not diagnose
// connection loss or replace pgxpool's idle-connection maintenance checks.
type HealthConfig struct {
	// IsEnabled starts periodic checks after the Fx connection's startup Ping.
	IsEnabled bool `env:"IS_ENABLED" envDescription:"Enable Fx shutdown on sustained pool health check failures; disabled by default."`
	// Interval is the delay before the first check and between completed checks.
	Interval time.Duration `env:"INTERVAL" envDescription:"Delay between pool health checks; required and positive when enabled."`
	// Timeout bounds each check, including waiting for an available connection.
	Timeout time.Duration `env:"TIMEOUT" envDescription:"Health check deadline including pool acquisition; positive and at most Interval."`
	// FailureAfter is measured from the completion of the first significant failure.
	// Success or an error outside the health policy resets the window. Shutdown
	// is requested on a significant failure at or beyond this duration, so it
	// is not an exact wall-clock deadline.
	FailureAfter time.Duration `env:"FAILURE_AFTER" envDescription:"Continuous failure window before Fx shutdown; at least Interval."`
}

// runHealthMonitor owns no pool resources. Its owner cancels ctx and waits for
// it to return before closing the pool. check must honor its context; shutdown
// must only request termination, not synchronously stop and join this monitor.
func runHealthMonitor(
	ctx context.Context,
	cfg HealthConfig,
	check func(context.Context) error,
	shutdown func() error,
	logger *slog.Logger,
	codes ...string,
) {
	timer := time.NewTimer(cfg.Interval)
	defer timer.Stop()

	var failedAt time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if ctx.Err() != nil {
			return
		}

		checkCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		err := check(checkCtx)
		cancel()

		if ctx.Err() != nil {
			return
		}

		category, code := classifyHealthError(err, codes)
		checkedAt := time.Now()

		switch {
		case err == nil:
			if !failedAt.IsZero() {
				logger.InfoContext(ctx, "database health restored")
				failedAt = time.Time{}
			}
		case len(category) == 0:
			failedAt = time.Time{}
		case failedAt.IsZero():
			failedAt = checkedAt
			// Never log the raw error: connection errors can contain credentials.
			logger.WarnContext(
				ctx,
				"database health check failed",
				"category", category,
				"sqlstate", code,
			)
		case checkedAt.Sub(failedAt) >= cfg.FailureAfter:
			logger.ErrorContext(
				ctx,
				"database health shutdown requested",
				"category", category,
				"sqlstate", code,
			)
			if err := shutdown(); err != nil {
				logger.ErrorContext(ctx, "database health shutdown request failed")
			}

			return
		}

		timer.Reset(cfg.Interval)
	}
}
