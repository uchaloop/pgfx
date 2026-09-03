package pgfx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

type warehouse struct {
	ID      int64  `db:"id"      json:"id"`
	CityEng string `db:"city_eng" json:"cityEng"`
}

// scriptedQuerier answers the statements of one page in order and records what
// it was asked.
type scriptedQuerier struct {
	rows []pgx.Rows
	err  error

	sql  []string
	args [][]any
}

func (q *scriptedQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.sql = append(q.sql, sql)
	q.args = append(q.args, args)

	if q.err != nil {
		return nil, q.err
	}

	return q.rows[len(q.sql)-1], nil
}

func warehouseQuery(t *testing.T) page.Query {
	t.Helper()

	query, err := page.Make(
		"SELECT w.id, w.city_eng FROM warehouses w WHERE w.country = $1;",
		page.Tie(page.Asc("id")),
	)
	if err != nil {
		t.Fatalf("page.Make: %v", err)
	}

	return query
}

func warehouseRows(ids ...int64) *fakeRows {
	values := make([][]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, []any{id, "Istanbul"})
	}

	return newFakeRows([]string{"id", "city_eng"}, values...)
}

func TestCollectPageShortPageSkipsCount(t *testing.T) {
	q := &scriptedQuerier{rows: []pgx.Rows{warehouseRows(41, 42)}}

	rows, total, err := collectPage[warehouse](
		context.Background(), q, warehouseQuery(t),
		page.Request{Number: 3, Size: 5}, []any{"TR"},
	)
	if err != nil {
		t.Fatalf("collectPage: %v", err)
	}

	if len(q.sql) != 1 {
		t.Fatalf("statements = %d, want 1 (a short page counts itself)", len(q.sql))
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Offset 10 plus the two rows that came back where five were asked for.
	if total != 12 {
		t.Fatalf("total = %d, want 12", total)
	}

	wantTail := `ORDER BY "id" ASC LIMIT $2 OFFSET $3`
	if !strings.HasSuffix(q.sql[0], wantTail) {
		t.Fatalf("rows statement = %q, want it to end with %q", q.sql[0], wantTail)
	}
	if strings.Contains(q.sql[0], ";") {
		t.Fatalf("rows statement = %q, want the trailing semicolon stripped", q.sql[0])
	}

	wantArgs := []any{"TR", uint(5), uint(10)}
	if len(q.args[0]) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", q.args[0], wantArgs)
	}
	for i, want := range wantArgs {
		if q.args[0][i] != want {
			t.Fatalf("args = %v, want %v", q.args[0], wantArgs)
		}
	}
}

func TestCollectPageFullPageCounts(t *testing.T) {
	q := &scriptedQuerier{rows: []pgx.Rows{
		warehouseRows(1, 2),
		newFakeRows([]string{"count"}, []any{int64(97)}),
	}}

	rows, total, err := collectPage[warehouse](
		context.Background(), q, warehouseQuery(t),
		page.Request{Number: 1, Size: 2}, []any{"TR"},
	)
	if err != nil {
		t.Fatalf("collectPage: %v", err)
	}

	if len(rows) != 2 || total != 97 {
		t.Fatalf("rows = %d, total = %d, want 2 and 97", len(rows), total)
	}
	if len(q.sql) != 2 {
		t.Fatalf("statements = %d, want 2 (a full page says nothing about the total)", len(q.sql))
	}
	if !strings.HasPrefix(q.sql[1], "SELECT count(*) FROM (") {
		t.Fatalf("count statement = %q", q.sql[1])
	}
	if len(q.args[1]) != 1 || q.args[1][0] != "TR" {
		t.Fatalf("count args = %v, want the query args alone", q.args[1])
	}
}

func TestCollectPageEmptyPageCounts(t *testing.T) {
	q := &scriptedQuerier{rows: []pgx.Rows{
		warehouseRows(),
		newFakeRows([]string{"count"}, []any{int64(4)}),
	}}

	rows, total, err := collectPage[warehouse](
		context.Background(), q, warehouseQuery(t),
		page.Request{Number: 9, Size: 5}, []any{"TR"},
	)
	if err != nil {
		t.Fatalf("collectPage: %v", err)
	}

	// An empty page says nothing: it may sit past the end of a much larger set.
	if len(rows) != 0 || total != 4 {
		t.Fatalf("rows = %d, total = %d, want 0 and 4", len(rows), total)
	}
	if len(q.sql) != 2 {
		t.Fatalf("statements = %d, want 2", len(q.sql))
	}
}

func TestCollectPageSortsByModelTag(t *testing.T) {
	q := &scriptedQuerier{rows: []pgx.Rows{warehouseRows(1)}}

	_, _, err := collectPage[warehouse](
		context.Background(), q, warehouseQuery(t),
		page.Request{Number: 1, Size: 5, Sort: []string{"city_eng:desc"}}, []any{"TR"},
	)
	if err != nil {
		t.Fatalf("collectPage: %v", err)
	}

	want := `ORDER BY "city_eng" DESC, "id" ASC`
	if !strings.Contains(q.sql[0], want) {
		t.Fatalf("rows statement = %q, want it to contain %q", q.sql[0], want)
	}
}

func TestCollectPageRejectsUnknownSortField(t *testing.T) {
	q := &scriptedQuerier{}

	_, _, err := collectPage[warehouse](
		context.Background(), q, warehouseQuery(t),
		page.Request{Number: 1, Size: 5, Sort: []string{"secret"}}, []any{"TR"},
	)
	if !errors.Is(err, page.ErrUnknownSortField) {
		t.Fatalf("err = %v, want ErrUnknownSortField", err)
	}
	if len(q.sql) != 0 {
		t.Fatalf("statements = %d, want none", len(q.sql))
	}
}

func TestCollectPageRejectsZeroRequest(t *testing.T) {
	q := &scriptedQuerier{}

	_, _, err := collectPage[warehouse](
		context.Background(), q, warehouseQuery(t),
		page.Request{Number: 1}, []any{"TR"},
	)
	if !errors.Is(err, page.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if len(q.sql) != 0 {
		t.Fatalf("statements = %d, want none", len(q.sql))
	}
}

func TestCountContextRenamesTheQuery(t *testing.T) {
	ctx := WithQueryName(context.Background(), "warehouses.list")
	if got := QueryName(countContext(ctx)); got != "warehouses.list.count" {
		t.Fatalf("name = %q, want %q", got, "warehouses.list.count")
	}

	// An unnamed query stays unnamed: ".count" alone would be a label about
	// nothing.
	if got := QueryName(countContext(context.Background())); len(got) != 0 {
		t.Fatalf("name = %q, want empty", got)
	}
}
