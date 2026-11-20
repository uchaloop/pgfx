package page

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestPagerPolicy(t *testing.T) {
	l, err := MakeLimits(5, 100, 20)
	if err != nil {
		t.Fatal(err)
	}

	fields := SortFields{"officeID": "o.id", "name": "o.name"}
	p, err := MakePager(l, SortConfig{Fields: fields, Tie: "o.id"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		size  int
		keys  []string
		limit int64
		order string
	}{
		{name: "defaults", limit: 20, order: "o.id ASC"},
		{
			name:  "first recognized field",
			size:  1,
			keys:  []string{"bad", " NAME : DeSc", "officeID"},
			limit: 5,
			order: "o.name DESC, o.id DESC",
		},
		{
			name:  "maximum size",
			size:  101,
			keys:  []string{"officeID:unknown"},
			limit: 100,
			order: "o.id ASC",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := p.Resolve(2, tc.size, tc.keys)
			if err != nil {
				t.Fatal(err)
			}

			b, order := s.PageParts()
			if b.Limit != tc.limit || b.Offset != tc.limit {
				t.Fatalf("bounds = %+v, want limit and offset %d", b, tc.limit)
			}

			if order != tc.order {
				t.Fatalf("order = %q, want %q", order, tc.order)
			}
		})
	}
}

func TestPagerCopiesSortFields(t *testing.T) {
	limits, err := MakeLimits(1, 100, 10)
	if err != nil {
		t.Fatal(err)
	}

	fields := SortFields{"name": "name"}
	pager, err := MakePager(limits, SortConfig{Fields: fields, Tie: "id"})
	if err != nil {
		t.Fatal(err)
	}

	fields["name"] = "other"
	selection, err := pager.Resolve(1, 10, []string{"name"})
	if err != nil {
		t.Fatal(err)
	}

	_, order := selection.PageParts()
	if order != "name ASC, id ASC" {
		t.Fatalf("order = %q", order)
	}
}

func TestPagerOffsetOverflow(t *testing.T) {
	limits, err := MakeLimits(1, 100, 10)
	if err != nil {
		t.Fatal(err)
	}

	pager, err := MakePager(limits, SortConfig{Tie: "id"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pager.Resolve(math.MaxInt, 100, nil); !errors.Is(err, ErrBounds) {
		t.Fatal(err)
	}
}

func TestInvalidLimits(t *testing.T) {
	if _, err := MakeLimits(0, 1, 1); err == nil {
		t.Fatal("invalid limits accepted")
	}
}

func TestIndependentPagers(t *testing.T) {
	for _, size := range []int{5, 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()

			l, err := MakeLimits(1, 100, size)
			if err != nil {
				t.Fatal(err)
			}

			p, err := MakePager(l, SortConfig{Tie: "id"})
			if err != nil {
				t.Fatal(err)
			}

			for range 4 {
				t.Run("resolve", func(t *testing.T) {
					t.Parallel()

					s, err := p.Resolve(1, 0, nil)
					if err != nil {
						t.Fatal(err)
					}

					b, _ := s.PageParts()
					if b.Limit != int64(size) {
						t.Fatal(b)
					}
				})
			}
		})
	}
}

func TestMaxOffset(t *testing.T) {
	base, err := MakeLimits(1, 100, 20)
	if err != nil {
		t.Fatal(err)
	}

	limits, err := base.WithMaxOffset(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		number, size int
		isRejected   bool
	}{
		{number: 2, size: 101}, {number: 7, size: 0, isRejected: true}, {number: math.MaxInt, size: 100, isRejected: true},
	} {
		_, err := limits.Bounds(tc.number, tc.size)
		if errors.Is(err, ErrBounds) != tc.isRejected || err != nil && !tc.isRejected {
			t.Fatalf("bounds(%d,%d): %v", tc.number, tc.size, err)
		}
	}
	if _, err := base.Bounds(7, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := base.WithMaxOffset(-1); !errors.Is(err, ErrBounds) {
		t.Fatal(err)
	}
}

func TestPagerOnlyUsesConfiguredOrder(t *testing.T) {
	limits, err := MakeLimits(1, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	pager, err := MakePager(limits, SortConfig{Fields: SortFields{"label": "item_name"}, Tie: "id"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := pager.Resolve(1, 10, []string{"unlisted:desc", "label:unrecognized"})
	if err != nil {
		t.Fatal(err)
	}
	_, order := s.PageParts()
	if order != "item_name ASC, id ASC" {
		t.Fatal(order)
	}
}
