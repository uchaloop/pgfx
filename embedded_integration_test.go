//go:build integration

package pgfx_test

import (
	"errors"
	"testing"
	"time"

	"github.com/uchaloop/pgfx/page"
)

func TestEmbeddedStructPaginationIntegration(t *testing.T) {
	type Point struct {
		X int64 `db:"x" json:"axisX"`
	}
	type row struct {
		Point `db:"point" json:"point"`
		ID    int64 `db:"id" json:"id"`
	}

	ctx, db, cleanup := makeIntegrationDB(t, 10*time.Second)
	defer cleanup()

	const sql = "SELECT * FROM (VALUES (1::bigint, 20::bigint), (2, 10)) AS items(id, x)"
	control, err := db.FetchRows[row](ctx, sql)
	if err != nil || len(control) != 2 || control[0].X != 20 {
		t.Fatalf("decode control: %v, %v", control, err)
	}

	for _, tc := range []struct {
		tag  string
		sort string
	}{
		{tag: "db", sort: "x"},
		{tag: "json", sort: "axisX"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			q := page.MakeQueryOrPanic(sql, page.WithTie(page.MakeAscOrder("id")), page.WithSortKeyTag(tc.tag))
			result, err := db.FetchPage[row](ctx, q, page.Request{Page: 1, Limit: 1, Sort: []string{tc.sort}})
			if err != nil {
				t.Fatal(err)
			}

			if len(result.List) != 1 || result.List[0].ID != 2 || result.List[0].X != 10 || result.Total != 2 {
				t.Fatalf("offset: %+v", result)
			}

			next, err := db.FetchPage[row](ctx, q, page.Request{Page: 2, Limit: 1, Sort: []string{tc.sort}})
			if err != nil {
				t.Fatal(err)
			}

			if len(next.List) != 1 || next.List[0].ID != 1 || next.List[0].X != 20 || next.Total != 2 {
				t.Fatalf("next page: %+v", next)
			}

			_, err = db.FetchPage[row](ctx, q, page.Request{Page: 1, Limit: 1, Sort: []string{"point"}})
			if !errors.Is(err, page.ErrUnknownSortField) {
				t.Fatalf("embedding tag accepted as column: %v", err)
			}
		})
	}
}
