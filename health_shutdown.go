package pgfx

import (
	"errors"
	"sync"

	"go.uber.org/fx"
)

// ErrHealthModuleRequired reports an enabled health monitor without the shared
// shutdown adapter. Add HealthModule once at the root of fx.New.
var ErrHealthModuleRequired = errors.New("enabled health checks require HealthModule at the application root")

// HealthModule serializes calls to fx.Shutdowner for the entire application.
// Add it once directly to fx.New, not inside fx.Module, when Config.Health is
// enabled. All consumers must use the decorated shutdowner from this scope;
// do not replace it or retain the underlying shutdowner in another decorator.
// It preserves Fx shutdown options, errors and signal ordering: a health failure
// has no priority over another concurrent shutdown signal. Enabled health checks
// without this adapter fail construction with [ErrHealthModuleRequired].
// Disabled checks need no adapter. This option adds no background goroutine.
var HealthModule = fx.Decorate(makeHealthShutdowner)

// healthShutdowner protects the mutable exit code in Fx 1.24's shutdowner.
// The lock belongs to the application, not to individual connection monitors.
type healthShutdowner struct {
	mu   sync.Mutex
	base fx.Shutdowner
}

var _ fx.Shutdowner = (*healthShutdowner)(nil)

func makeHealthShutdowner(base fx.Shutdowner) fx.Shutdowner {
	return &healthShutdowner{base: base}
}

func (s *healthShutdowner) Shutdown(opts ...fx.ShutdownOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.base.Shutdown(opts...)
}
