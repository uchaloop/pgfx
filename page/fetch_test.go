package page_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/uchaloop/pgfx/page"
)

func TestFetchSequential(t *testing.T) {
	type contextKey struct{}

	for _, items := range [][]int{nil, {}, {3, 5}} {
		var calls []string

		ctx := context.WithValue(t.Context(), contextKey{}, "value")
		got, err := page.Fetch(ctx, func(gotCtx context.Context) ([]int, error) {
			if gotCtx != ctx {
				t.Fatal("list context changed")
			}

			calls = append(calls, "list")

			return items, nil
		}, func(gotCtx context.Context) (int64, error) {
			if gotCtx != ctx {
				t.Fatal("count context changed")
			}

			calls = append(calls, "count")

			return 12, nil
		})
		if err != nil || got.List == nil || !slices.Equal(got.List, items) || got.Total != 12 {
			t.Fatalf("got %+v, %v", got, err)
		}

		if !slices.Equal(calls, []string{"list", "count"}) {
			t.Fatalf("calls: %v", calls)
		}
	}
}

func TestFetchFailures(t *testing.T) {
	errFailure := errors.New("query failed")

	for _, name := range []string{"list", "count", "nil list", "nil count", "canceled", "cancel after list", "cancel after count"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			calls := 0
			list := func(context.Context) ([]int, error) {
				calls++

				if name == "list" {
					return []int{1}, errFailure
				}

				if name == "cancel after list" {
					cancel()
				}

				return []int{1}, nil
			}
			count := func(context.Context) (int64, error) {
				calls++

				if name == "count" {
					return 8, errFailure
				}

				if name == "cancel after count" {
					cancel()
				}

				return 8, nil
			}

			switch name {
			case "nil list":
				list = nil
			case "nil count":
				count = nil
			case "canceled":
				cancel()
			}

			got, err := page.Fetch(ctx, list, count)
			if err == nil || got.List != nil || got.Total != 0 {
				t.Fatalf("got %+v, %v", got, err)
			}

			switch name {
			case "list", "count":
				if !errors.Is(err, errFailure) {
					t.Fatalf("lost cause: %v", err)
				}
			case "canceled", "cancel after list", "cancel after count":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			}

			want := 2

			switch name {
			case "list", "cancel after list":
				want = 1
			case "nil list", "nil count", "canceled":
				want = 0
			}

			if calls != want {
				t.Fatalf("calls=%d, want %d", calls, want)
			}
		})
	}
}

func TestFetchParallel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listStarted, countStarted := make(chan struct{}), make(chan struct{})
		got, err := page.Fetch(
			t.Context(),
			func(ctx context.Context) ([]int, error) {
				close(listStarted)
				<-countStarted

				return []int{7}, nil
			},
			func(ctx context.Context) (int64, error) {
				close(countStarted)
				<-listStarted

				return 19, nil
			},
			page.WithParallel(),
		)
		if err != nil || !slices.Equal(got.List, []int{7}) || got.Total != 19 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestFetchParallelCancelsSiblingAndWaits(t *testing.T) {
	for _, isListFailure := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			errFailure := errors.New("first failure")
			siblingStarted, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			ctx := t.Context()
			run := func(ctx context.Context, isFailure bool) error {
				if isFailure {
					<-siblingStarted

					return errFailure
				}

				close(siblingStarted)
				<-ctx.Done()

				if !errors.Is(context.Cause(ctx), errFailure) {
					t.Errorf("lost cancellation cause: %v", context.Cause(ctx))
				}

				<-release

				return ctx.Err()
			}

			go func() {
				defer close(done)

				got, err := page.Fetch(
					ctx,
					func(ctx context.Context) ([]int, error) { return []int{1}, run(ctx, isListFailure) },
					func(ctx context.Context) (int64, error) { return 9, run(ctx, !isListFailure) },
					page.WithParallel(),
				)
				if !errors.Is(err, errFailure) || got.List != nil || got.Total != 0 {
					t.Errorf("got %+v, %v", got, err)
				}
			}()

			synctest.Wait()

			select {
			case <-done:
				t.Fatal("returned before sibling exited")
			default:
			}

			close(release)
			<-done

			if ctx.Err() != nil {
				t.Fatal("parent context canceled")
			}
		})
	}
}

func TestFetchParallelParentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		started := make(chan struct{}, 2)
		done := make(chan struct{})

		go func() {
			defer close(done)

			got, err := page.Fetch(
				ctx,
				func(ctx context.Context) ([]int, error) {
					started <- struct{}{}
					<-ctx.Done()

					return []int{1}, nil
				},
				func(ctx context.Context) (int64, error) {
					started <- struct{}{}
					<-ctx.Done()

					return 2, nil
				},
				page.WithParallel(),
			)
			if !errors.Is(err, context.Canceled) || got.List != nil || got.Total != 0 {
				t.Errorf("got %+v, %v", got, err)
			}
		}()
		<-started
		<-started
		cancel()
		<-done
	})
}

func TestFetchParallelEmptyList(t *testing.T) {
	got, err := page.Fetch(
		t.Context(),
		func(context.Context) ([]int, error) { return nil, nil },
		func(context.Context) (int64, error) { return 15, nil },
		nil,
		page.WithParallel(),
		page.WithParallel(),
	)
	if err != nil || got.List == nil || len(got.List) != 0 || got.Total != 15 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
