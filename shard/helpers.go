package shard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/uchaloop/pgfx"
)

// afterRoute is a test hook called by Read right after Route; nil in production.
var afterRoute func()

// ShardResult is the outcome of a write on one shard.
type ShardResult struct {
	Shard string
	Err   error
}

// Write runs write on every shard the key's route writes to, in order: the
// source, then the target while the bucket is migrating. Each call runs inside
// the shard's Attempt and reports its error with Done, so a transaction inside
// write counts as one operation. A failure on the source does not stop the
// target; a shard whose breaker refuses is skipped with an error matching
// ErrShardUnavailable. The error joins the failures as "write <shard>: …".
//
// Writes on different shards are not atomic. write must be safe to retry and,
// for concurrent changes to one key, ordered by a version check in SQL or by
// per-key serialization; see the package documentation.
func (r *Router[K]) Write(ctx context.Context, key K, write func(context.Context, *pgfx.DB) error) ([]ShardResult, error) {
	route, err := r.Route(key)
	if err != nil {
		return nil, err
	}

	results := make([]ShardResult, 0, len(route.Writes))

	var failures []error

	for _, target := range route.Writes {
		err := runAttempt(ctx, target, write)
		results = append(results, ShardResult{Shard: target.Shard, Err: err})

		if err != nil {
			failures = append(failures, fmt.Errorf("write %s: %w", target.Shard, err))
		}
	}

	return results, errors.Join(failures...)
}

// Read reads the key on its source shard: the master first, then the replicas,
// moving on when a connection is unavailable or its breaker refuses (see
// pgfx.ReadWithFallback). Each read runs inside the connection's Attempt.
// When every connection refuses, the error matches ErrShardUnavailable.
// Replica data may lag behind the master.
func (r *Router[K]) Read[T any](ctx context.Context, key K, read func(context.Context, *pgfx.DB) (T, error)) (T, error) {
	route, err := r.Route(key)
	if err != nil {
		var zero T

		return zero, err
	}

	if afterRoute != nil {
		afterRoute()
	}

	return readDBs(ctx, route.Reads, read)
}

// Read reads from the shard as Router.Read does for a key: the master first,
// then the replicas.
func (s Shard) Read[T any](ctx context.Context, read func(context.Context, *pgfx.DB) (T, error)) (T, error) {
	return readDBs(ctx, append([]*pgfx.DB{s.Master}, s.Replicas...), read)
}

// Each calls fn for every shard of the current layout, at most limit at a time
// (limit < 1 runs all at once), and waits for all of them. It does not consult
// breakers: fn chooses Target.Attempt for writes or Shard.Read for reads, so a
// shard with a failed master can still be read from its replicas. The error
// joins the failures as "shard <name>: …". A panic in fn is re-raised after
// every call has returned.
func (r *Router[K]) Each(ctx context.Context, limit int, fn func(context.Context, Shard) error) error {
	shards := r.Shards()

	if limit < 1 || limit > len(shards) {
		limit = len(shards)
	}

	var (
		mu        sync.Mutex
		failures  []error
		recovered any
		wg        sync.WaitGroup
	)

	slots := make(chan struct{}, max(limit, 1))

	for _, s := range shards {
		slots <- struct{}{}

		wg.Go(func() {
			defer func() { <-slots }()
			defer func() {
				if p := recover(); p != nil {
					mu.Lock()
					recovered = p
					mu.Unlock()
				}
			}()

			if err := fn(ctx, s); err != nil {
				mu.Lock()
				failures = append(failures, fmt.Errorf("shard %s: %w", s.Name, err))
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	if recovered != nil {
		panic(recovered)
	}

	return errors.Join(failures...)
}

// Partition is a batch grouped by write destination, built from one layout
// snapshot.
type Partition[T any] struct {
	// Version is the layout version the batch was grouped with; groups that
	// are not written yet belong to operations of this version.
	Version int64
	// Groups has one group per destination shard, sorted by shard name.
	Groups []Group[T]
}

// Group is the part of a batch that goes to one shard.
type Group[T any] struct {
	// Target is the destination; the caller runs Target.Attempt.
	Target Target
	// Items are the batch items for this shard, in input order.
	Items []T
	// Indexes are the positions of Items in the input slice.
	Indexes []int
}

// Partition groups items by the shards their keys write to, using one layout
// snapshot taken before the first key call. While a bucket migrates, its items
// appear in the source and the target group. It only groups: no SQL, no
// Attempt, no retries, and no execution order between groups - unlike Write,
// the caller may write the target before the source.
//
// Items and Indexes are new slices, but items are copied shallowly: pointers,
// maps and slices inside T stay shared, and the caller must not change them
// concurrently. Duplicate keys are kept; a batched INSERT … ON CONFLICT DO
// UPDATE fails when one key appears twice, so collapse duplicates first -
// keeping the highest version is valid only for full snapshots, not for
// deltas or increments. Any bucket outside the layout rejects the whole batch
// with ErrLayout. key must be non-nil.
func (r *Router[K]) Partition[T any](items []T, key func(T) K) (Partition[T], error) {
	if key == nil {
		return Partition[T]{}, errors.New("nil key function")
	}

	layout := r.layout.Load()
	groups := make(map[string]*Group[T])

	for i, item := range items {
		_, place, err := r.placement(layout, key(item))
		if err != nil {
			return Partition[T]{}, err
		}

		for _, name := range []string{place.source, place.target} {
			if len(name) == 0 {
				continue
			}

			group, isOK := groups[name]
			if !isOK {
				group = &Group[T]{Target: Target{Shard: name, DB: r.dbs[layout.nodes[name].Master]}}
				groups[name] = group
			}

			group.Items = append(group.Items, item)
			group.Indexes = append(group.Indexes, i)
		}
	}

	partition := Partition[T]{Version: layout.Version(), Groups: make([]Group[T], 0, len(groups))}

	for _, group := range groups {
		partition.Groups = append(partition.Groups, *group)
	}

	slices.SortFunc(partition.Groups, func(a, b Group[T]) int { return strings.Compare(a.Target.Shard, b.Target.Shard) })

	return partition, nil
}

// runAttempt runs fn on the target inside its Attempt. A panic releases the
// permit without a verdict and propagates.
func runAttempt(ctx context.Context, target Target, fn func(context.Context, *pgfx.DB) error) error {
	ctx, attempt, err := target.Attempt(ctx)
	if err != nil {
		return err
	}

	isFinished := false

	defer func() {
		if !isFinished {
			attempt.Release()
		}
	}()

	err = fn(ctx, target.DB)
	isFinished = true
	attempt.Done(err)

	return err
}

// readDBs reads with fallback over dbs, running each read inside the
// connection's Attempt. When every connection refuses, the error also matches
// ErrShardUnavailable.
func readDBs[T any](ctx context.Context, dbs []*pgfx.DB, read func(context.Context, *pgfx.DB) (T, error)) (T, error) {
	refused := 0

	value, err := pgfx.ReadWithFallback(ctx, dbs, func(ctx context.Context, db *pgfx.DB) (T, error) {
		ctx, attempt, err := db.Breaker().Attempt(ctx)
		if err != nil {
			refused++

			var zero T

			return zero, err
		}

		isFinished := false

		defer func() {
			if !isFinished {
				attempt.Release()
			}
		}()

		value, err := read(ctx, db)
		isFinished = true
		attempt.Done(err)

		return value, err
	})

	if err != nil && refused == len(dbs) {
		return value, fmt.Errorf("%w: %w", ErrShardUnavailable, err)
	}

	return value, err
}
