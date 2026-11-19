package shard

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrLayout reports an invalid layout, a bucket outside the layout, or a
// forbidden transition between layouts.
var ErrLayout = errors.New("invalid shard layout")

// reservedChars separate the parts of the text forms and may not appear in
// shard or connection names.
const reservedChars = ":|>;,"

// NodeSpec is the composition of one shard: its name, the pgfx connection name
// of its master and the connection names of its replicas. Its text form is
// "shard:master|replica-1|replica-2".
type NodeSpec struct {
	Shard    string
	Master   string
	Replicas []string
}

// MarshalText implements encoding.TextMarshaler.
func (n NodeSpec) MarshalText() ([]byte, error) {
	return []byte(n.Shard + ":" + strings.Join(append([]string{n.Master}, n.Replicas...), "|")), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (n *NodeSpec) UnmarshalText(text []byte) error {
	shard, members, isOK := strings.Cut(string(text), ":")
	if !isOK || len(shard) == 0 || len(members) == 0 {
		return fmt.Errorf("invalid shard node %q: want shard:master|replica", text)
	}

	connections := strings.Split(members, "|")
	if len(connections[0]) == 0 {
		return fmt.Errorf("invalid shard node %q: empty master", text)
	}

	*n = NodeSpec{Shard: shard, Master: connections[0], Replicas: connections[1:]}

	return nil
}

// RangeSpec assigns buckets to a shard. With a Target the buckets are migrating
// from Shard to Target: writes go to both, reads come from Shard. Its text form
// is "shard:buckets" or "source>target:buckets", for example "node2>node3:512-600".
type RangeSpec struct {
	Shard   string
	Target  string
	Buckets BucketSet
}

// MarshalText implements encoding.TextMarshaler.
func (r RangeSpec) MarshalText() ([]byte, error) {
	owner := r.Shard
	if len(r.Target) != 0 {
		owner += ">" + r.Target
	}

	return []byte(owner + ":" + r.Buckets.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *RangeSpec) UnmarshalText(text []byte) error {
	owner, bucketsText, isOK := strings.Cut(string(text), ":")
	if !isOK || len(strings.TrimSpace(bucketsText)) == 0 {
		return fmt.Errorf("invalid bucket range %q: want shard:buckets", text)
	}

	shard, target, isMigrating := strings.Cut(owner, ">")
	if len(shard) == 0 {
		return fmt.Errorf("invalid bucket range %q: empty shard", text)
	}

	if isMigrating && len(target) == 0 {
		return fmt.Errorf("invalid bucket range %q: empty migration target", text)
	}

	buckets, err := ParseBucketSet(bucketsText)
	if err != nil {
		return err
	}

	*r = RangeSpec{Shard: shard, Target: target, Buckets: buckets}

	return nil
}

// LayoutConfig is the serializable layout. Its parts are text values, so ENV
// loaders (confmaker, caarlos0/env), JSON and YAML read it the same way:
//
//	SHARDS_VERSION=7
//	SHARDS_BUCKETS=1024
//	SHARDS_NODES=node1:node1;node2:node2;node3:node3|node3-replica-1
//	SHARDS_RANGES=node1:0-340;node2:341-511;node2>node3:512-600;node3:601-1023
type LayoutConfig struct {
	// Version increases with every published layout; Router.Update refuses an
	// older one.
	Version int64 `json:"version" yaml:"version" env:"VERSION" envDescription:"Layout version; must grow with every published layout."`

	// Buckets is the number of buckets and never changes.
	Buckets int `json:"buckets" yaml:"buckets" env:"BUCKETS" envDescription:"Number of buckets; never changes once data is written."`

	// Nodes lists every shard and its connections.
	Nodes []NodeSpec `json:"nodes" yaml:"nodes" env:"NODES" envSeparator:";" envDescription:"Shards as shard:master|replica, separated by semicolons."`

	// Ranges assigns every bucket to a shard, or to a source and a target.
	Ranges []RangeSpec `json:"ranges" yaml:"ranges" env:"RANGES" envSeparator:";" envDescription:"Bucket ranges as shard:buckets or source>target:buckets, separated by semicolons."`
}

// EnvPrefix declares the default ENV prefix for loaders that support it, as
// pgfx.Config does with POSTGRES_. A named instance "example" reads EXAMPLE_SHARDS_*.
func (LayoutConfig) EnvPrefix() string {
	return "SHARDS_"
}

// Validate runs the same checks as MakeLayout, so a loader can reject a bad
// layout together with the rest of the configuration.
func (c LayoutConfig) Validate() error {
	_, err := MakeLayout(c)

	return err
}

// placement is where one bucket lives: its source shard and, while migrating,
// its target shard.
type placement struct {
	source string
	target string
}

// Layout is an immutable, validated layout.
type Layout struct {
	version    int64
	nodes      map[string]NodeSpec
	shards     []string
	placements []placement
}

// MakeLayout validates cfg and returns an immutable layout. Every problem is
// reported at once in an error matching ErrLayout: bucket ranges that leave
// gaps, overlap or fall outside the bucket count; ranges naming unknown shards
// or migrating a shard to itself; shards without a master or listed twice; a
// connection used twice; reserved characters (: | > ; ,) in names.
func MakeLayout(cfg LayoutConfig) (*Layout, error) {
	var problems []error

	if cfg.Buckets < 1 {
		return nil, fmt.Errorf("%w: buckets must be positive, got %d", ErrLayout, cfg.Buckets)
	}

	nodes := make(map[string]NodeSpec, len(cfg.Nodes))
	owners := make(map[string]string)

	for _, node := range cfg.Nodes {
		problems = append(problems, checkName(node.Shard))

		if _, isRepeated := nodes[node.Shard]; isRepeated {
			problems = append(problems, fmt.Errorf("shard %s is repeated", node.Shard))

			continue
		}

		if len(node.Master) == 0 {
			problems = append(problems, fmt.Errorf("shard %s has no master", node.Shard))
		}

		node.Replicas = slices.Clone(node.Replicas)
		nodes[node.Shard] = node

		for _, connection := range append([]string{node.Master}, node.Replicas...) {
			problems = append(problems, checkName(connection))

			switch owner, isUsed := owners[connection]; {
			case !isUsed:
				owners[connection] = node.Shard
			case owner == node.Shard:
				problems = append(problems, fmt.Errorf("shard %s lists connection %s twice", node.Shard, connection))
			default:
				problems = append(problems, fmt.Errorf("connection %s is used by shards %s and %s", connection, owner, node.Shard))
			}
		}
	}

	placements := make([]placement, cfg.Buckets)

	for _, spec := range cfg.Ranges {
		text, _ := spec.MarshalText()

		problems = append(problems, checkRange(spec, nodes, cfg.Buckets, string(text))...)

		var overlaps []BucketRange

		for _, bucketRange := range spec.Buckets.Ranges() {
			for bucket := max(bucketRange.From, 0); bucket <= min(bucketRange.To, cfg.Buckets-1); bucket++ {
				if len(placements[bucket].source) != 0 {
					overlaps = append(overlaps, BucketRange{From: bucket, To: bucket})

					continue
				}

				placements[bucket] = placement{source: spec.Shard, target: spec.Target}
			}
		}

		if len(overlaps) != 0 {
			problems = append(problems, fmt.Errorf("range %s: buckets %s overlap another range", text, makeBucketSet(overlaps)))
		}
	}

	var gaps []BucketRange

	for bucket, place := range placements {
		if len(place.source) == 0 {
			gaps = append(gaps, BucketRange{From: bucket, To: bucket})
		}
	}

	if len(gaps) != 0 {
		problems = append(problems, fmt.Errorf("buckets %s are not assigned to a shard", makeBucketSet(gaps)))
	}

	if err := errors.Join(problems...); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLayout, err)
	}

	shards := make([]string, 0, len(nodes))
	for shard := range nodes {
		shards = append(shards, shard)
	}

	slices.Sort(shards)

	return &Layout{version: cfg.Version, nodes: nodes, shards: shards, placements: placements}, nil
}

// Version returns the layout version.
func (l *Layout) Version() int64 {
	return l.version
}

// Buckets returns the number of buckets.
func (l *Layout) Buckets() int {
	return len(l.placements)
}

// Shards returns the shard names, sorted.
func (l *Layout) Shards() []string {
	return slices.Clone(l.shards)
}

// connections returns every connection name the layout references.
func (l *Layout) connections() []string {
	var names []string

	for _, shard := range l.shards {
		node := l.nodes[shard]
		names = append(names, node.Master)
		names = append(names, node.Replicas...)
	}

	return names
}

// ValidateTransition checks the shape of a change from old to new: the bucket
// count stays the same, and every bucket either keeps its placement, starts
// migrating from its shard (active A -> migrating A>B), finishes migrating
// (A>B -> active B) or rolls back (A>B -> active A). A direct move
// (active A -> active B) and a change of target are refused, naming the
// buckets.
//
// It checks only the shape. A safe move also needs every writer to apply each
// version and finish operations started with the previous one before the next
// step, or a store that rejects writes made with an old version.
func ValidateTransition(old, new *Layout) error {
	if old == nil || new == nil {
		return fmt.Errorf("%w: nil layout", ErrLayout)
	}

	if old.Buckets() != new.Buckets() {
		return fmt.Errorf("%w: bucket count changes from %d to %d", ErrLayout, old.Buckets(), new.Buckets())
	}

	var forbidden []BucketRange

	for bucket, before := range old.placements {
		if !isAllowedStep(before, new.placements[bucket]) {
			forbidden = append(forbidden, BucketRange{From: bucket, To: bucket})
		}
	}

	if len(forbidden) != 0 {
		return fmt.Errorf("%w: buckets %s change placement without migrating", ErrLayout, makeBucketSet(forbidden))
	}

	return nil
}

func isAllowedStep(before, after placement) bool {
	switch {
	case before == after:
		return true
	case len(before.target) == 0:
		return after.source == before.source && len(after.target) != 0
	default:
		return len(after.target) == 0 && (after.source == before.target || after.source == before.source)
	}
}

func checkName(name string) error {
	if strings.ContainsAny(name, reservedChars) {
		return fmt.Errorf("name %q contains one of the reserved characters %s", name, reservedChars)
	}

	return nil
}

func checkRange(spec RangeSpec, nodes map[string]NodeSpec, buckets int, text string) []error {
	var problems []error

	for _, shard := range []string{spec.Shard, spec.Target} {
		if len(shard) == 0 {
			continue
		}

		problems = append(problems, checkName(shard))

		if _, isKnown := nodes[shard]; !isKnown {
			problems = append(problems, fmt.Errorf("range %s names unknown shard %s", text, shard))
		}
	}

	if len(spec.Target) != 0 && spec.Target == spec.Shard {
		problems = append(problems, fmt.Errorf("range %s migrates shard %s to itself", text, spec.Shard))
	}

	for _, bucketRange := range spec.Buckets.Ranges() {
		if bucketRange.To >= buckets {
			problems = append(problems, fmt.Errorf("range %s is outside 0-%d", text, buckets-1))
		}
	}

	return problems
}
