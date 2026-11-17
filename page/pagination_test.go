package page_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
)

func TestBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		params  page.Params
		config  page.Config
		want    page.Bounds
		isError bool
	}{
		{
			name:   "minimum",
			params: page.Params{Limit: 2},
			config: page.Config{MinLimit: 5},
			want:   page.Bounds{Limit: 5},
		},
		{name: "invalid minimum", config: page.Config{MinLimit: 11}, isError: true},
		{name: "negative minimum", config: page.Config{MinLimit: -1}, isError: true},
		{name: "defaults", want: page.Bounds{Limit: 10}},
		{name: "negative", params: page.Params{Page: -1, Limit: -1}, want: page.Bounds{Limit: 10}},
		{name: "cap", params: page.Params{Page: 2, Limit: 200}, want: page.Bounds{Limit: 100, Offset: 100}},
		{
			name:   "custom",
			params: page.Params{Page: 3},
			config: page.Config{DefaultLimit: 5, MaxLimit: 20},
			want:   page.Bounds{Limit: 5, Offset: 10},
		},
		{name: "overflow", params: page.Params{Page: math.MaxInt, Limit: 100}, isError: true},
		{name: "invalid config", config: page.Config{DefaultLimit: 20, MaxLimit: 10}, isError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pager, err := page.MakePager(tc.config)
			var got page.Bounds
			if err == nil {
				got, err = pager.Bounds(tc.params)
			}
			if errors.Is(err, page.ErrParams) != tc.isError || got != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestFetchPageRejectsParamsBeforeDatabase(t *testing.T) {
	pager, err := page.MakePager(page.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []page.Params{{Page: math.MaxInt, Limit: 100}} {
		result, err := pager.Fetch(
			t.Context(),
			nil,
			params,
			func(context.Context, *pgfx.Tx, page.Bounds) ([]int, error) {
				t.Fatal("list called for invalid parameters")
				return nil, nil
			},
			func(context.Context, *pgfx.Tx) (int64, error) {
				t.Fatal("count called for invalid parameters")
				return 0, nil
			},
		)
		if !errors.Is(err, page.ErrParams) || result.List != nil || result.Total != 0 {
			t.Fatalf("result %+v: %v", result, err)
		}
	}
}
