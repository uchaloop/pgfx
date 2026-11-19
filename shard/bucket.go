package shard

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Buckets maps a key to a bucket in [0, Count()). The function must be
// deterministic across processes and releases: every service that writes or
// reads a key must compute the same bucket.
type Buckets[K any] struct {
	count  int
	bucket func(K) int
}

// Bucket returns the bucket of key.
func (b Buckets[K]) Bucket(key K) int {
	return b.bucket(key)
}

// Count returns the number of buckets.
func (b Buckets[K]) Count() int {
	return b.count
}

// ModuloBuckets maps an integer key to key % count, made non-negative for
// negative keys. It panics if count is below 1 or does not fit K.
func ModuloBuckets[K ~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64](count int) Buckets[K] {
	n := K(count)
	if count < 1 || int(n) != count {
		panic(fmt.Sprintf("invalid bucket count %d", count))
	}

	return Buckets[K]{count: count, bucket: func(key K) int {
		r := key % n
		if r < 0 {
			r += n
		}

		return int(r)
	}}
}

// StringBuckets maps a string key to FNV-1a 64 of its bytes, modulo count.
// The hash is fixed, so buckets stay the same across processes and Go
// releases. It panics if count is below 1.
func StringBuckets[K ~string](count int) Buckets[K] {
	if count < 1 {
		panic(fmt.Sprintf("invalid bucket count %d", count))
	}

	return Buckets[K]{count: count, bucket: func(key K) int {
		const (
			offset = 14695981039346656037
			prime  = 1099511628211
		)

		hash := uint64(offset)

		for i := range len(key) {
			hash ^= uint64(key[i])
			hash *= prime
		}

		return int(hash % uint64(count))
	}}
}

// MakeBuckets wraps an application formula. bucket must return a value in
// [0, count) and be deterministic across processes: hash/maphash and map
// hashing are seeded per process and therefore unsuitable. It panics if count
// is below 1 or bucket is nil.
func MakeBuckets[K any](count int, bucket func(K) int) Buckets[K] {
	if count < 1 || bucket == nil {
		panic(fmt.Sprintf("invalid buckets: count %d, nil func %t", count, bucket == nil))
	}

	return Buckets[K]{count: count, bucket: bucket}
}

// BucketRange is an inclusive range of buckets.
type BucketRange struct {
	From int
	To   int
}

// BucketSet is a normalized set of bucket ranges: sorted, with overlapping and
// adjacent ranges merged. Its text form is "0-170,512-600,777". The zero value
// is empty.
type BucketSet struct {
	ranges []BucketRange
}

// ParseBucketSet parses comma-separated ranges ("0-170") and single buckets
// ("777"). Spaces around items are ignored and "" is the empty set.
func ParseBucketSet(text string) (BucketSet, error) {
	var ranges []BucketRange

	for item := range strings.SplitSeq(text, ",") {
		item = strings.TrimSpace(item)
		if len(item) == 0 {
			if len(strings.TrimSpace(text)) == 0 {
				break
			}

			return BucketSet{}, fmt.Errorf("empty bucket range in %q", text)
		}

		bucketRange, err := parseBucketRange(item)
		if err != nil {
			return BucketSet{}, err
		}

		ranges = append(ranges, bucketRange)
	}

	return makeBucketSet(ranges), nil
}

// Contains reports whether bucket is in the set.
func (s BucketSet) Contains(bucket int) bool {
	i := sort.Search(len(s.ranges), func(i int) bool { return s.ranges[i].To >= bucket })

	return i < len(s.ranges) && s.ranges[i].From <= bucket
}

// Ranges returns a copy of the normalized ranges.
func (s BucketSet) Ranges() []BucketRange {
	return slices.Clone(s.ranges)
}

// String returns the canonical text form.
func (s BucketSet) String() string {
	items := make([]string, len(s.ranges))

	for i, bucketRange := range s.ranges {
		if bucketRange.From == bucketRange.To {
			items[i] = strconv.Itoa(bucketRange.From)
		} else {
			items[i] = strconv.Itoa(bucketRange.From) + "-" + strconv.Itoa(bucketRange.To)
		}
	}

	return strings.Join(items, ",")
}

// MarshalText implements encoding.TextMarshaler.
func (s BucketSet) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler, so configuration loaders
// read a BucketSet from one string.
func (s *BucketSet) UnmarshalText(text []byte) error {
	set, err := ParseBucketSet(string(text))
	if err != nil {
		return err
	}

	*s = set

	return nil
}

func parseBucketRange(item string) (BucketRange, error) {
	fromText, toText, isRange := strings.Cut(item, "-")

	from, err := parseBucket(fromText, item)
	if err != nil {
		return BucketRange{}, err
	}

	to := from

	if isRange {
		if to, err = parseBucket(toText, item); err != nil {
			return BucketRange{}, err
		}
	}

	if from > to {
		return BucketRange{}, fmt.Errorf("bucket range %q is reversed", item)
	}

	return BucketRange{From: from, To: to}, nil
}

func parseBucket(text, item string) (int, error) {
	bucket, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || bucket < 0 {
		return 0, errors.Join(fmt.Errorf("invalid bucket range %q", item), err)
	}

	return bucket, nil
}

func makeBucketSet(ranges []BucketRange) BucketSet {
	slices.SortFunc(ranges, func(a, b BucketRange) int { return a.From - b.From })

	merged := make([]BucketRange, 0, len(ranges))

	for _, bucketRange := range ranges {
		last := len(merged) - 1
		if last >= 0 && bucketRange.From <= merged[last].To+1 {
			merged[last].To = max(merged[last].To, bucketRange.To)

			continue
		}

		merged = append(merged, bucketRange)
	}

	return BucketSet{ranges: merged}
}
