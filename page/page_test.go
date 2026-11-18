package page_test

import (
	"errors"
	"math"
	"testing"

	"github.com/uchaloop/pgfx/page"
)

func TestMakeBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		number int
		size   int
		want   page.Bounds
	}{
		{name: "first", number: 1, size: 20, want: page.Bounds{Limit: 20}},
		{name: "third", number: 3, size: 20, want: page.Bounds{Limit: 20, Offset: 40}},
		{name: "largest", number: math.MaxInt64/10 + 1, size: 10, want: page.Bounds{Limit: 10, Offset: math.MaxInt64 / 10 * 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := page.MakeBounds(tc.number, tc.size)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestMakeBoundsRejectsInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		number int
		size   int
	}{
		{name: "zero page", size: 20},
		{name: "zero size", number: 1},
		{name: "negative", number: -1, size: 5},
		{name: "overflow", number: math.MaxInt64/10 + 2, size: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := page.MakeBounds(tc.number, tc.size)
			if !errors.Is(err, page.ErrBounds) || got != (page.Bounds{}) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}
