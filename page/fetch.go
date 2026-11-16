package page

import (
	"context"
	"errors"

	"golang.org/x/sync/errgroup"
)

// Result contains the requested list and the total number of matching items.
// List is non-nil on success and retains the slice returned by the list callback.
type Result[T any] struct {
	List  []T
	Total int64
}

type config struct {
	isParallel bool
}

// Option configures Fetch. Nil options are ignored.
type Option func(*config)

// WithParallel runs list and count concurrently. Repeated use has no extra effect.
// Both callbacks and their shared resources must support concurrent use.
// In particular, do not share a single database transaction or connection.
func WithParallel() Option {
	return func(cfg *config) { cfg.isParallel = true }
}

// Fetch combines list and count without creating a transaction or changing SQL.
// By default it calls list, then count, including when list is empty. A list
// error skips count. Any error returns a zero Result and preserves its cause.
// Both callbacks must be non-nil; ctx must be non-nil. Callbacks must honor
// context cancellation. A canceled context prevents work from starting.
//
// WithParallel supplies both callbacks a derived context, cancels it on the
// first registered callback error, and waits for both callbacks before returning
// that error. Simultaneous errors have no guaranteed ordering. Parent cancellation
// is returned if no callback fails. Fetch cannot interrupt a callback that ignores
// its context, so cancellation does not remove the obligation to wait.
// The first cancellation determines context.Cause: a callback error is used
// unless the parent context was canceled first. The derived context is also
// canceled after both callbacks finish, including on success.
func Fetch[T any](ctx context.Context, list func(context.Context) ([]T, error), count func(context.Context) (int64, error), opts ...Option) (Result[T], error) {
	if list == nil {
		return Result[T]{}, errors.New("list callback is nil")
	}

	if count == nil {
		return Result[T]{}, errors.New("count callback is nil")
	}

	if err := ctx.Err(); err != nil {
		return Result[T]{}, err
	}

	var cfg config

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	fetch := fetchSequential[T]

	if cfg.isParallel {
		fetch = fetchParallel[T]
	}

	result, err := fetch(ctx, list, count)
	if err != nil {
		return Result[T]{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result[T]{}, err
	}

	if result.List == nil {
		result.List = []T{}
	}

	return result, nil
}

func fetchSequential[T any](ctx context.Context, list func(context.Context) ([]T, error), count func(context.Context) (int64, error)) (Result[T], error) {
	items, err := list(ctx)
	if err != nil {
		return Result[T]{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result[T]{}, err
	}

	total, err := count(ctx)
	if err != nil {
		return Result[T]{}, err
	}

	return Result[T]{List: items, Total: total}, nil
}

func fetchParallel[T any](ctx context.Context, list func(context.Context) ([]T, error), count func(context.Context) (int64, error)) (Result[T], error) {
	g, ctx := errgroup.WithContext(ctx)

	var result Result[T]

	g.Go(func() error {
		var err error
		result.List, err = list(ctx)

		return err
	})

	g.Go(func() error {
		var err error
		result.Total, err = count(ctx)

		return err
	})

	if err := g.Wait(); err != nil {
		return Result[T]{}, err
	}

	return result, nil
}
