package pgfx

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// BreakerConfig enables a per-connection circuit breaker. Failures == 0
// disables it. When enabled, Failures consecutive failed operations open the
// breaker for OpenFor; after that a single probe operation decides whether it
// closes again.
type BreakerConfig struct {
	// Failures is the number of consecutive failed operations that opens the
	// breaker. Zero disables the breaker.
	Failures int `env:"FAILURES" envDescription:"Consecutive failed operations that open the connection breaker; zero disables it."`

	// OpenFor is how long the breaker refuses operations before it lets one
	// probe through. It also bounds how long an unfinished probe holds its
	// permit.
	OpenFor time.Duration `env:"OPEN_FOR" envDescription:"How long an open breaker refuses operations before one probe; required when failures is set."`
}

type breakerState uint8

const (
	stateClosed breakerState = iota
	stateOpen
)

// Breaker is a per-connection circuit breaker created with the connection from
// Config.Breaker; DB.Breaker returns it.
//
// An operation asks for a permit with Attempt and reports its outcome with
// Done or Release. Failures (unavailability, timeouts, SQLSTATE 57014) count;
// a server answer, including an SQL error, resets the count; cancellation and
// application errors are neutral. Queries that do not run inside an Attempt
// are counted command by command through the connection's tracer, so a
// transaction should run inside an Attempt to count as one operation.
//
// An open breaker does not block queries issued directly through DB: callers
// check Attempt first. While half-open, only the probe's owner decides the
// next state.
type Breaker struct {
	cfg BreakerConfig
	now func() time.Time

	mu         sync.Mutex
	state      breakerState
	failures   int
	generation uint64
	openedAt   time.Time
	probe      *Attempt
	probeAt    time.Time
}

// Attempt is the permit of one operation. Its first Done or Release ends it;
// later calls do nothing.
type Attempt struct {
	breaker    *Breaker
	generation uint64
	isNested   bool
	isFinished atomic.Bool
}

// attemptKey marks a context with the unfinished operation of one breaker.
type attemptKey struct {
	breaker *Breaker
}

// outcome is what an operation's result means for the breaker.
type outcome uint8

const (
	outcomeNeutral outcome = iota
	outcomeFailure
	outcomeSuccess
)

func makeBreaker(cfg BreakerConfig) *Breaker {
	if cfg.Failures == 0 {
		return nil
	}

	return &Breaker{cfg: cfg, now: time.Now}
}

// Breaker returns the connection's breaker, or nil when Config.Breaker is
// disabled or the DB was assembled outside pgfx.
func (d *DB) Breaker() *Breaker {
	if d.state == nil {
		return nil
	}

	return d.state.breaker
}

// Attempt returns a permit for one operation, or ErrBreakerOpen.
//
// A closed breaker always grants. An open breaker refuses until OpenFor has
// passed; then it grants one probe at a time, and a probe that is not
// finished within OpenFor is replaced by a new one.
//
// The returned context marks this breaker's operation while it is unfinished:
// the connection's tracer does not count queries started with it, and an
// Attempt with that context on the same breaker is nested - it gets no probe
// and its Done does nothing, because the outer operation reports the outcome.
// After Done or Release the mark is inert.
//
// Done(nil) means the operation really used the database: validate input and
// return early before Attempt, and finish queries and transactions before
// calling Done.
//
// A nil Breaker (disabled) grants every Attempt and records nothing.
func (b *Breaker) Attempt(ctx context.Context) (context.Context, *Attempt, error) {
	if b == nil {
		return ctx, &Attempt{isNested: true}, nil
	}

	if outer, isOK := ctx.Value(attemptKey{breaker: b}).(*Attempt); isOK && !outer.isFinished.Load() {
		return ctx, &Attempt{isNested: true}, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	attempt := &Attempt{breaker: b, generation: b.generation}

	if b.state == stateOpen {
		now := b.now()

		if now.Before(b.openedAt.Add(b.cfg.OpenFor)) {
			return ctx, nil, ErrBreakerOpen
		}

		if b.probe != nil && now.Before(b.probeAt.Add(b.cfg.OpenFor)) {
			return ctx, nil, ErrBreakerOpen
		}

		b.probe, b.probeAt = attempt, now
	}

	return context.WithValue(ctx, attemptKey{breaker: b}, attempt), attempt, nil
}

// Done ends the operation with its result: a failure counts towards opening,
// a server answer (nil or an SQL error) is a success, and cancellation or an
// application error is neutral. Only the first Done or Release counts.
func (a *Attempt) Done(err error) {
	a.finish(classify(err))
}

// Release ends the operation without a verdict, for example after a panic.
// It frees a held probe permit and leaves the failure count unchanged.
func (a *Attempt) Release() {
	a.finish(outcomeNeutral)
}

func (a *Attempt) finish(result outcome) {
	if a == nil || a.isNested || !a.isFinished.CompareAndSwap(false, true) {
		return
	}

	a.breaker.settle(a, result)
}

// settle applies an operation's outcome. While open, only the current probe
// decides; while closed, only operations of the current generation count.
func (b *Breaker) settle(attempt *Attempt, result outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == stateOpen {
		if b.probe != attempt {
			return
		}

		b.probe = nil

		switch result {
		case outcomeFailure:
			b.open()
		case outcomeSuccess:
			b.close()
		}

		return
	}

	if attempt.generation == b.generation {
		b.count(result)
	}
}

// record applies a tracer event of the given generation. Events change only a
// closed breaker; while open or half-open the probe's owner decides.
func (b *Breaker) record(generation uint64, result outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == stateClosed && generation == b.generation {
		b.count(result)
	}
}

// currentGeneration returns the generation new tracer events belong to.
func (b *Breaker) currentGeneration() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.generation
}

// isSuppressed reports whether ctx carries an unfinished operation of b, whose
// own Done reports the outcome.
func (b *Breaker) isSuppressed(ctx context.Context) bool {
	attempt, isOK := ctx.Value(attemptKey{breaker: b}).(*Attempt)

	return isOK && !attempt.isFinished.Load()
}

func (b *Breaker) count(result outcome) {
	switch result {
	case outcomeFailure:
		b.failures++

		if b.failures >= b.cfg.Failures {
			b.open()
		}
	case outcomeSuccess:
		b.failures = 0
	}
}

func (b *Breaker) open() {
	b.state = stateOpen
	b.openedAt = b.now()
	b.failures = 0
	b.generation++
	b.probe = nil
}

func (b *Breaker) close() {
	b.state = stateClosed
	b.failures = 0
	b.generation++
}

// classify reads err with the breaker policy, in priority order: cancellation
// anywhere is neutral; any failing part (unavailability, a deadline, a timeout,
// SQLSTATE 57014) is a failure; a server answer is a success; nil is a success;
// anything else is neutral. Decoding errors stop the walk, so an error inside
// pgx.ScanArgError is neither a failure nor a success.
func classify(err error) outcome {
	if err == nil {
		return outcomeSuccess
	}

	var isCanceled, isFailure, isServer bool

	walkError(err, false, func(leaf errorLeaf) {
		switch leaf.kind {
		case leafCanceled:
			isCanceled = true
		case leafDeadline, leafTLS, leafClosed, leafNetwork:
			isFailure = true
		case leafServer:
			_, isUnavailable := unavailableStates[leaf.code]
			if isUnavailable || leaf.code == "57014" {
				isFailure = true
			} else {
				isServer = true
			}
		case leafOther:
			isFailure = isFailure || leaf.isConnect
		}
	})

	switch {
	case isCanceled:
		return outcomeNeutral
	case isFailure:
		return outcomeFailure
	case isServer:
		return outcomeSuccess
	default:
		return outcomeNeutral
	}
}
