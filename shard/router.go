package shard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/uchaloop/pgfx"
)

// ErrShardUnavailable reports a shard whose master breaker refuses the
// operation; the error also matches pgfx.ErrBreakerOpen.
var ErrShardUnavailable = errors.New("shard unavailable")

// Router routes keys to shards with one immutable layout snapshot per call.
// It is safe for concurrent use; Update swaps the layout atomically.
type Router[K any] struct {
	buckets Buckets[K]
	dbs     map[string]*pgfx.DB

	mu     sync.Mutex
	layout atomic.Pointer[Layout]
}

// Route is where one key lives in one layout snapshot.
type Route struct {
	// Version is the version of the layout the route was built from.
	Version int64
	// Bucket is the key's bucket.
	Bucket int
	// Writes are the shards a write goes to: the source, then the target while
	// the bucket is migrating.
	Writes []Target
	// Reads are the source shard's master, then its replicas.
	Reads []*pgfx.DB
}

// Target is a shard to write to and its master connection.
type Target struct {
	Shard string
	DB    *pgfx.DB
}

// Shard is one shard of the current layout.
type Shard struct {
	Name     string
	Master   *pgfx.DB
	Replicas []*pgfx.DB
}

// MakeRouter returns a router over layout. buckets must have the layout's
// bucket count. dbs are the pgfx connections, matched by Name: every
// connection the layout references must be present and have a breaker
// (Config.Breaker), names must be unique and non-empty. Connections the layout
// does not reference yet may be listed for later layouts. MakeRouter changes
// nothing when it fails.
func MakeRouter[K any](buckets Buckets[K], layout *Layout, dbs []*pgfx.DB) (*Router[K], error) {
	if layout == nil {
		return nil, fmt.Errorf("%w: nil layout", ErrLayout)
	}

	if buckets.Count() != layout.Buckets() {
		return nil, fmt.Errorf("%w: buckets count %d differs from layout count %d", ErrLayout, buckets.Count(), layout.Buckets())
	}

	byName := make(map[string]*pgfx.DB, len(dbs))

	for _, db := range dbs {
		if db == nil {
			return nil, errors.New("nil database")
		}

		name := db.Name()
		if len(name) == 0 {
			return nil, errors.New("database has no name: create it with MakeModuleFor or WithName")
		}

		if _, isDuplicate := byName[name]; isDuplicate {
			return nil, fmt.Errorf("database name %s is repeated", name)
		}

		byName[name] = db
	}

	router := &Router[K]{buckets: buckets, dbs: byName}

	if err := router.checkConnections(layout); err != nil {
		return nil, err
	}

	router.layout.Store(layout)

	return router, nil
}

// Update replaces the layout. The new layout must be a permitted transition
// from the current one (ValidateTransition), carry a higher Version, and
// reference only known connections with breakers. Concurrent calls are
// serialized; Route never waits for Update.
func (r *Router[K]) Update(layout *Layout) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.layout.Load()

	if err := ValidateTransition(current, layout); err != nil {
		return err
	}

	if layout.Version() <= current.Version() {
		return fmt.Errorf("%w: version %d is not newer than %d", ErrLayout, layout.Version(), current.Version())
	}

	if err := r.checkConnections(layout); err != nil {
		return err
	}

	r.layout.Store(layout)

	return nil
}

// Version returns the version of the current layout.
func (r *Router[K]) Version() int64 {
	return r.layout.Load().Version()
}

// Route returns where key lives in the current layout. A bucket outside the
// layout returns an error matching ErrLayout.
func (r *Router[K]) Route(key K) (Route, error) {
	return r.route(r.layout.Load(), key)
}

// Shards returns the shards of the current layout, sorted by name.
func (r *Router[K]) Shards() []Shard {
	layout := r.layout.Load()
	shards := make([]Shard, 0, len(layout.shards))

	for _, name := range layout.shards {
		shards = append(shards, r.shard(layout, name))
	}

	return shards
}

// Attempt asks the target's master breaker for a permit; see pgfx.Breaker.
// A refusal matches both ErrShardUnavailable and pgfx.ErrBreakerOpen.
func (t Target) Attempt(ctx context.Context) (context.Context, *pgfx.Attempt, error) {
	ctx, attempt, err := t.DB.Breaker().Attempt(ctx)
	if err != nil {
		return ctx, nil, fmt.Errorf("%w: %s: %w", ErrShardUnavailable, t.Shard, err)
	}

	return ctx, attempt, nil
}

func (r *Router[K]) route(layout *Layout, key K) (Route, error) {
	bucket, place, err := r.placement(layout, key)
	if err != nil {
		return Route{}, err
	}

	source := r.shard(layout, place.source)

	route := Route{
		Version: layout.Version(),
		Bucket:  bucket,
		Writes:  []Target{{Shard: source.Name, DB: source.Master}},
		Reads:   append([]*pgfx.DB{source.Master}, source.Replicas...),
	}

	if len(place.target) != 0 {
		route.Writes = append(route.Writes, Target{Shard: place.target, DB: r.dbs[layout.nodes[place.target].Master]})
	}

	return route, nil
}

// placement returns the key's bucket and where it lives in layout.
func (r *Router[K]) placement(layout *Layout, key K) (int, placement, error) {
	bucket := r.buckets.Bucket(key)
	if bucket < 0 || bucket >= layout.Buckets() {
		return 0, placement{}, fmt.Errorf("%w: bucket %d is outside 0-%d", ErrLayout, bucket, layout.Buckets()-1)
	}

	return bucket, layout.placements[bucket], nil
}

func (r *Router[K]) shard(layout *Layout, name string) Shard {
	node := layout.nodes[name]
	replicas := make([]*pgfx.DB, len(node.Replicas))

	for i, replica := range node.Replicas {
		replicas[i] = r.dbs[replica]
	}

	return Shard{Name: name, Master: r.dbs[node.Master], Replicas: replicas}
}

func (r *Router[K]) checkConnections(layout *Layout) error {
	for _, name := range layout.connections() {
		db, isKnown := r.dbs[name]
		if !isKnown {
			return fmt.Errorf("%w: connection %s is not among the router's databases", ErrLayout, name)
		}

		if db.Breaker() == nil {
			return fmt.Errorf("connection %s has no breaker: set Config.Breaker", name)
		}
	}

	return nil
}
