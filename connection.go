package pgfx

import "errors"

// ErrNoConnState reports a DB assembled outside MakeDB, MakeModule and
// MakeModuleFor, for example &DB{Pool: pool}. Such a DB works for queries but
// has no name and no breaker.
var ErrNoConnState = errors.New("database was not created by pgfx")

// connState is the per-connection state shared by a DB and its tracer. It is
// built once with the connection and never replaced.
type connState struct {
	name    string
	breaker *Breaker
}

// WithName names the connection. The name labels fallback errors and the
// Connection field of QueryMetric, so keep it short and bounded, for example
// "node3" or "node3-replica". MakeModuleFor sets the name from its own argument
// and ignores WithName.
func WithName(name string) Option {
	return func(o *options) { o.name = name }
}

// Name returns the connection name, or "" when the connection is unnamed or
// was assembled outside pgfx.
func (d *DB) Name() string {
	if d.state == nil {
		return ""
	}

	return d.state.name
}
