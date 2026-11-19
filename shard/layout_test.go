package shard_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/uchaloop/pgfx/shard"
)

func parseNodes(t *testing.T, text string) []shard.NodeSpec {
	t.Helper()

	var nodes []shard.NodeSpec

	for item := range strings.SplitSeq(text, ";") {
		var node shard.NodeSpec
		if err := node.UnmarshalText([]byte(item)); err != nil {
			t.Fatal(err)
		}

		nodes = append(nodes, node)
	}

	return nodes
}

func parseRanges(t *testing.T, text string) []shard.RangeSpec {
	t.Helper()

	var ranges []shard.RangeSpec

	for item := range strings.SplitSeq(text, ";") {
		var spec shard.RangeSpec
		if err := spec.UnmarshalText([]byte(item)); err != nil {
			t.Fatal(err)
		}

		ranges = append(ranges, spec)
	}

	return ranges
}

func makeLayout(t *testing.T, version int64, buckets int, nodes, ranges string) *shard.Layout {
	t.Helper()

	layout, err := shard.MakeLayout(shard.LayoutConfig{
		Version: version,
		Buckets: buckets,
		Nodes:   parseNodes(t, nodes),
		Ranges:  parseRanges(t, ranges),
	})
	if err != nil {
		t.Fatal(err)
	}

	return layout
}

func TestNodeSpecText(t *testing.T) {
	var node shard.NodeSpec
	if err := node.UnmarshalText([]byte("node3:node3|node3-replica-1")); err != nil {
		t.Fatal(err)
	}

	if node.Shard != "node3" || node.Master != "node3" || !slices.Equal(node.Replicas, []string{"node3-replica-1"}) {
		t.Fatalf("node = %+v", node)
	}

	text, err := node.MarshalText()
	if err != nil || string(text) != "node3:node3|node3-replica-1" {
		t.Fatalf("MarshalText = %s, %v", text, err)
	}

	for _, bad := range []string{"node3:", ":x", "node3"} {
		if err := new(shard.NodeSpec).UnmarshalText([]byte(bad)); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestRangeSpecText(t *testing.T) {
	var migrating shard.RangeSpec
	if err := migrating.UnmarshalText([]byte("node2>node3:512-600")); err != nil {
		t.Fatal(err)
	}

	if migrating.Shard != "node2" || migrating.Target != "node3" || migrating.Buckets.String() != "512-600" {
		t.Fatalf("migrating = %+v", migrating)
	}

	var active shard.RangeSpec
	if err := active.UnmarshalText([]byte("node1:0-340")); err != nil || len(active.Target) != 0 {
		t.Fatalf("active = %+v, %v", active, err)
	}

	text, err := migrating.MarshalText()
	if err != nil || string(text) != "node2>node3:512-600" {
		t.Fatalf("MarshalText = %s, %v", text, err)
	}

	for _, bad := range []string{"node1:", ":0-3", "node1", "a>:0-3"} {
		if err := new(shard.RangeSpec).UnmarshalText([]byte(bad)); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestLayoutConfigEnvShape(t *testing.T) {
	layout := makeLayout(t, 1, 1024, "node1:node1;node3:node3|node3-replica-1", "node1:0-511;node3:512-1023")

	if !slices.Equal(layout.Shards(), []string{"node1", "node3"}) {
		t.Fatalf("Shards = %v", layout.Shards())
	}
}

func TestMakeLayout(t *testing.T) {
	layout := makeLayout(t, 7, 1024, "node1:node1;node2:node2;node3:node3", "node1:0-511;node2>node3:512-1023")

	if layout.Version() != 7 || layout.Buckets() != 1024 {
		t.Fatalf("Version = %d, Buckets = %d", layout.Version(), layout.Buckets())
	}
}

func TestMakeLayoutErrors(t *testing.T) {
	_, err := shard.MakeLayout(shard.LayoutConfig{
		Buckets: 16,
		Nodes: append(
			parseNodes(t, "node1:node1;node2:node2;node3:node3|node3;node1:node9"),
			shard.NodeSpec{Shard: "node4"},
		),
		Ranges: parseRanges(t, "node1:0-5;node2:4-9;node2>node2:10-11;node1>node8:12-13"),
	})
	if !errors.Is(err, shard.ErrLayout) {
		t.Fatalf("err = %v", err)
	}

	for _, want := range []string{
		"14-15",             // gap
		"overlap",           // node2:4-9 overlaps node1:0-5
		"node8",             // unknown target
		"node2 to itself",   // node2>node2
		"node4",             // empty master
		"node1 is repeated", // repeated node
		"node3 twice",       // node3:node3|node3
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestMakeLayoutRejectsReservedNames(t *testing.T) {
	_, err := shard.MakeLayout(shard.LayoutConfig{
		Buckets: 1,
		Nodes:   []shard.NodeSpec{{Shard: "a,b", Master: "m"}},
		Ranges:  []shard.RangeSpec{{Shard: "a,b"}},
	})
	if !errors.Is(err, shard.ErrLayout) || !strings.Contains(err.Error(), "a,b") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTransition(t *testing.T) {
	const nodes = "node1:node1;node2:node2;node3:node3"

	active := makeLayout(t, 1, 4, nodes, "node1:0-3")
	migrating := makeLayout(t, 2, 4, nodes, "node1:0-1;node1>node2:2-3")
	moved := makeLayout(t, 3, 4, nodes, "node1:0-1;node2:2-3")
	jumped := makeLayout(t, 2, 4, nodes, "node1:0-1;node2:2-3")
	retargeted := makeLayout(t, 3, 4, nodes, "node1:0-1;node1>node3:2-3")
	resized := makeLayout(t, 2, 8, nodes, "node1:0-7")

	for _, tc := range []struct {
		name      string
		old, new  *shard.Layout
		isAllowed bool
	}{
		{name: "unchanged", old: active, new: active, isAllowed: true},
		{name: "start migrating", old: active, new: migrating, isAllowed: true},
		{name: "finish migrating", old: migrating, new: moved, isAllowed: true},
		{name: "roll back", old: migrating, new: active, isAllowed: true},
		{name: "jump", old: active, new: jumped},
		{name: "retarget", old: migrating, new: retargeted},
		{name: "resize", old: active, new: resized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := shard.ValidateTransition(tc.old, tc.new)
			if (err == nil) != tc.isAllowed {
				t.Fatalf("ValidateTransition = %v", err)
			}

			if err != nil && !errors.Is(err, shard.ErrLayout) {
				t.Fatalf("error %v does not match ErrLayout", err)
			}

			if tc.name == "jump" && !strings.Contains(err.Error(), "2-3") {
				t.Fatalf("error %q does not name the range", err)
			}
		})
	}
}

func TestLayoutConfigValidate(t *testing.T) {
	invalid := shard.LayoutConfig{Buckets: 4, Nodes: parseNodes(t, "a:a"), Ranges: parseRanges(t, "a:0-1")}

	_, makeErr := shard.MakeLayout(invalid)
	validateErr := invalid.Validate()

	if validateErr == nil || validateErr.Error() != makeErr.Error() {
		t.Fatalf("Validate = %v, MakeLayout = %v", validateErr, makeErr)
	}

	valid := shard.LayoutConfig{Buckets: 4, Nodes: parseNodes(t, "a:a"), Ranges: parseRanges(t, "a:0-3")}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLayoutConfigEnvPrefix(t *testing.T) {
	if prefix := (shard.LayoutConfig{}).EnvPrefix(); prefix != "SHARDS_" {
		t.Fatalf("EnvPrefix = %q", prefix)
	}
}

func TestLayoutConfigJSONShape(t *testing.T) {
	var cfg shard.LayoutConfig
	if err := json.Unmarshal([]byte(`{"version":7,"buckets":4,"nodes":["a:a"],"ranges":["a:0-3"]}`), &cfg); err != nil {
		t.Fatal(err)
	}

	layout, err := shard.MakeLayout(cfg)
	if err != nil || layout.Version() != 7 {
		t.Fatalf("layout = %v, %v", layout, err)
	}
}
