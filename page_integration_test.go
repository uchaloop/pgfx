//go:build integration

package pgfx

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/uchaloop/pgfx/page"
)

type pageItem struct {
	ID    int64  `db:"id"    json:"id"`
	Grade string `db:"grade" json:"grade"`
}

// TestFetchPageIntegration walks a table whose sort column repeats, which is
// where a page without a tie order starts losing and duplicating rows.
func TestFetchPageIntegration(t *testing.T) {
	host := os.Getenv("PGFX_TEST_HOST")
	if host == "" {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := Make(ctx, integrationConfig(host, 5*time.Second))
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	t.Cleanup(db.Close)

	const table = "pgfx_integration_pages"

	if _, err := db.Exec(ctx, "drop table if exists "+table); err != nil {
		t.Fatalf("drop stale table: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.Exec(cleanupCtx, "drop table if exists "+table)
	})

	if _, err := db.Exec(
		ctx,
		"create table "+table+" (id bigint primary key, grade text not null, country text not null)",
	); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Ten matching rows sharing three grades, plus one row the filter excludes.
	if _, err := db.Exec(
		ctx,
		"insert into "+table+" (id, grade, country) "+
			"select i, 'g' || (i % 3), 'TR' from generate_series(1, 10) AS i",
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if _, err := db.Exec(
		ctx,
		"insert into "+table+" (id, grade, country) values (99, 'g0', 'DE')",
	); err != nil {
		t.Fatalf("seed excluded row: %v", err)
	}

	query, err := page.Make(
		"SELECT id, grade FROM "+table+" WHERE country = $1;",
		page.Tie(page.Asc("id")),
		page.SortKeyTag("json"),
	)
	if err != nil {
		t.Fatalf("page.Make: %v", err)
	}

	seen := make(map[int64]int, 10)
	sort := []string{"grade:desc"}

	for number := uint(1); number <= 4; number++ {
		req := page.Request{Number: number, Size: 3, Sort: sort}

		items, total, err := db.FetchPage[pageItem](ctx, query, req, "TR")
		if err != nil {
			t.Fatalf("page %d: %v", number, err)
		}

		if total != 10 {
			t.Fatalf("page %d: total = %d, want 10", number, total)
		}

		want := 3
		if number == 4 {
			want = 1
		}
		if len(items) != want {
			t.Fatalf("page %d: rows = %d, want %d", number, len(items), want)
		}

		for _, item := range items {
			seen[item.ID]++
		}
	}

	// The fourth page is short, so its total came from the rows rather than from
	// count(*) - the two have to agree, and every row has to appear exactly once
	// across the four pages.
	if len(seen) != 10 {
		t.Fatalf("distinct rows = %d, want 10", len(seen))
	}
	for id, times := range seen {
		if times != 1 {
			t.Fatalf("row %d appeared %d times", id, times)
		}
	}

	// A page past the end is empty, not an error, and still reports the total.
	items, total, err := db.FetchPage[pageItem](
		ctx, query, page.Request{Number: 9, Size: 3, Sort: sort}, "TR",
	)
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}
	if len(items) != 0 || total != 10 {
		t.Fatalf("rows = %d, total = %d, want 0 and 10", len(items), total)
	}
}
