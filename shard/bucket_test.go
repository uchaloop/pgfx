package shard_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/uchaloop/pgfx/shard"
)

func TestModuloBuckets(t *testing.T) {
	buckets := shard.ModuloBuckets[int64](1024)

	for _, tc := range []struct {
		key  int64
		want int
	}{
		{key: 0, want: 0},
		{key: 1025, want: 1},
		{key: -1, want: 1023},
	} {
		if got := buckets.Bucket(tc.key); got != tc.want {
			t.Fatalf("Bucket(%d) = %d, want %d", tc.key, got, tc.want)
		}
	}

	for _, key := range []int64{math.MinInt64, math.MaxInt64} {
		if got := buckets.Bucket(key); got < 0 || got >= 1024 {
			t.Fatalf("Bucket(%d) = %d is out of range", key, got)
		}
	}

	if buckets.Count() != 1024 {
		t.Fatalf("Count = %d", buckets.Count())
	}
}

func TestModuloBucketsRejectsNarrow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a count that does not fit int32 was accepted")
		}
	}()

	shard.ModuloBuckets[int32](1 << 40)
}

func TestStringBucketsStable(t *testing.T) {
	buckets := shard.StringBuckets[string](1024)

	if got := buckets.Bucket(""); got != 805 {
		t.Fatalf(`Bucket("") = %d, want 805`, got)
	}

	if got := buckets.Bucket("tenant-42"); got != 482 {
		t.Fatalf(`Bucket("tenant-42") = %d, want 482`, got)
	}
}

func TestParseBucketSet(t *testing.T) {
	set, err := shard.ParseBucketSet("512-600, 0-170,171-200,777")
	if err != nil {
		t.Fatal(err)
	}

	if set.String() != "0-200,512-600,777" {
		t.Fatalf("String = %q", set.String())
	}

	if !set.Contains(200) || !set.Contains(777) || set.Contains(201) {
		t.Fatalf("Contains is wrong for %s", set)
	}

	empty, err := shard.ParseBucketSet("")
	if err != nil || len(empty.Ranges()) != 0 {
		t.Fatalf("empty set = %v, %v", empty, err)
	}
}

func TestParseBucketSetErrors(t *testing.T) {
	for _, text := range []string{"5-1", "-3", "a-b", "1-"} {
		if _, err := shard.ParseBucketSet(text); err == nil {
			t.Fatalf("%q was accepted", text)
		}
	}
}

func TestBucketSetText(t *testing.T) {
	set, err := shard.ParseBucketSet("512-600,0-200,777")
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(struct{ S shard.BucketSet }{S: set})
	if err != nil || string(data) != `{"S":"0-200,512-600,777"}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}

	var decoded struct{ S shard.BucketSet }
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.S.String() != set.String() {
		t.Fatalf("Unmarshal = %s, %v", decoded.S, err)
	}
}
