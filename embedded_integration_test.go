//go:build integration

package pgfx_test

import (
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

	const sql = `
		SELECT
			*
		FROM (VALUES(1::bigint, 20::bigint), (2, 10)) AS items(id, x)
	`
	control, err := db.FetchRows[row](ctx, sql)
	if err != nil || len(control) != 2 || control[0].X != 20 {
		t.Fatalf("decode control: %v, %v", control, err)
	}

	for _, offset := range []int64{0, 1} {
		result, err := db.FetchPage[row](
			ctx,
			`
				SELECT
					id,
					x,
					count(*) OVER () AS total
				FROM (`+sql+`) AS items
				ORDER BY x, id
				LIMIT @limit
				OFFSET @offset
			`,
			nil,
			page.Bounds{Limit: 1, Offset: offset},
		)
		if err != nil {
			t.Fatal(err)
		}

		wantID, wantX := int64(2), int64(10)
		if offset == 1 {
			wantID, wantX = 1, 20
		}

		if len(result.List) != 1 || result.List[0].ID != wantID || result.List[0].X != wantX || result.Total != 2 {
			t.Fatalf("page: %+v", result)
		}
	}
}
