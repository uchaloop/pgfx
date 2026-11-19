package main

import "testing"

func TestLatestItems(t *testing.T) {
	items := []item{{ID: 1, Version: 2, Payload: "new"}, {ID: 2, Version: 1, Payload: "other"}, {ID: 1, Version: 1, Payload: "old"}, {ID: 1, Version: 2, Payload: "new"}}
	got, err := latestItems(items)
	if err != nil || len(got) != 2 || got[0] != items[0] || got[1] != items[1] {
		t.Fatalf("collapse: %+v, %v", got, err)
	}

	// Even a stale conflicting version must be rejected before any group writes.
	items = append(items, item{ID: 1, Version: 1, Payload: "conflict"})
	if _, err := latestItems(items); err == nil {
		t.Fatal("conflicting equal versions accepted")
	}
}
