package pgfx

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx/page"
)

type pageItem struct {
	ID int64 `db:"id"`
}

func TestCollectPage(t *testing.T) {
	ctx := context.Background()
	args := pgx.NamedArgs{"tenant": "a"}
	rows := makeFakeRows([]string{"id", "total"}, []any{int64(1), int64(5)}, []any{int64(2), int64(5)})
	q := queryProbe(func(_ context.Context, _ string, gotArgs ...any) (pgx.Rows, error) {
		want := pgx.NamedArgs{"tenant": "a", "limit": int64(2), "offset": int64(0)}
		if len(gotArgs) != 1 || !reflect.DeepEqual(gotArgs[0], want) {
			t.Fatalf("args = %v", gotArgs)
		}

		return rows, nil
	})

	got, err := collectPage[pageItem](ctx, q, "select @limit, @offset", args, page.Bounds{Limit: 2})
	if err != nil || !reflect.DeepEqual(got, page.Result[pageItem]{List: []pageItem{{ID: 1}, {ID: 2}}, Total: 5}) {
		t.Fatalf("got %+v, %v", got, err)
	}

	if len(args) != 1 || !rows.isClosed {
		t.Fatalf("args = %v, closed = %v", args, rows.isClosed)
	}
}

func TestCollectPagePastTheEnd(t *testing.T) {
	var bounds []pgx.NamedArgs

	q := queryProbe(func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
		named := args[0].(pgx.NamedArgs)
		bounds = append(bounds, pgx.NamedArgs{"limit": named["limit"], "offset": named["offset"]})

		if named["offset"] == int64(0) {
			return makeFakeRows([]string{"id", "total"}, []any{int64(1), int64(5)}), nil
		}

		return makeFakeRows([]string{"id", "total"}), nil
	})

	got, err := collectPage[pageItem](context.Background(), q, "select @limit, @offset", nil, page.Bounds{Limit: 2, Offset: 10})
	if err != nil || got.List == nil || len(got.List) != 0 || got.Total != 5 {
		t.Fatalf("got %+v, %v", got, err)
	}

	want := []pgx.NamedArgs{{"limit": int64(2), "offset": int64(10)}, {"limit": int64(1), "offset": int64(0)}}
	if !reflect.DeepEqual(bounds, want) {
		t.Fatalf("bounds = %v", bounds)
	}
}

func TestCollectPageEmpty(t *testing.T) {
	calls := 0
	q := queryProbe(func(context.Context, string, ...any) (pgx.Rows, error) {
		calls++

		return makeFakeRows([]string{"id", "total"}), nil
	})

	got, err := collectPage[pageItem](context.Background(), q, "select @limit, @offset", nil, page.Bounds{Limit: 2})
	if err != nil || got.List == nil || len(got.List) != 0 || got.Total != 0 || calls != 1 {
		t.Fatalf("got %+v, %v, calls = %d", got, err, calls)
	}
}

func TestCollectPageRejectsBeforeQuery(t *testing.T) {
	q := fakeQuerier{err: errors.New("query ran")}

	for _, tc := range []struct {
		name   string
		sql    string
		args   pgx.NamedArgs
		bounds page.Bounds
		want   string
	}{
		{name: "no offset", sql: "select @limit", bounds: page.Bounds{Limit: 1}, want: "page query must use @limit and @offset"},
		{name: "no limit", sql: "select @offset", bounds: page.Bounds{Limit: 1}, want: "page query must use @limit and @offset"},
		{name: "limit arg", sql: "select @limit, @offset", args: pgx.NamedArgs{"limit": 1}, bounds: page.Bounds{Limit: 1}, want: "page args must not set limit or offset"},
		{name: "offset arg", sql: "select @limit, @offset", args: pgx.NamedArgs{"offset": 1}, bounds: page.Bounds{Limit: 1}, want: "page args must not set limit or offset"},
		{name: "zero limit", sql: "select @limit, @offset", want: page.ErrBounds.Error()},
		{name: "negative offset", sql: "select @limit, @offset", bounds: page.Bounds{Limit: 1, Offset: -1}, want: page.ErrBounds.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := collectPage[pageItem](context.Background(), q, tc.sql, tc.args, tc.bounds)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestCollectPageRequiresTotal(t *testing.T) {
	rows := makeFakeRows([]string{"id"}, []any{int64(1)})

	_, err := collectPage[pageItem](context.Background(), fakeQuerier{rows: rows}, "select @limit, @offset", nil, page.Bounds{Limit: 1})
	if err == nil || err.Error() != "page query must end with a total column" || !rows.isClosed {
		t.Fatalf("got %v, closed = %v", err, rows.isClosed)
	}
}

func TestCollectPageReturnsQueryError(t *testing.T) {
	queryErr := errors.New("syntax error")
	rows := makeFakeRows(nil)
	rows.err = queryErr

	_, err := collectPage[pageItem](context.Background(), fakeQuerier{rows: rows}, "select @limit, @offset", nil, page.Bounds{Limit: 1})
	if !errors.Is(err, queryErr) {
		t.Fatalf("got %v", err)
	}
}

func TestTotalRowHidesTotal(t *testing.T) {
	rows := makeFakeRows([]string{"id", "total"}, []any{int64(3), int64(9)})
	rows.Next()

	var id, total int64

	row := totalRow{Rows: rows, total: &total}
	values, err := row.Values()

	if len(row.FieldDescriptions()) != 1 || err != nil || !reflect.DeepEqual(values, []any{int64(3)}) {
		t.Fatalf("fields = %v, values = %v, %v", row.FieldDescriptions(), values, err)
	}

	if err := row.Scan(&id); err != nil || id != 3 || total != 9 {
		t.Fatalf("id = %d, total = %d, %v", id, total, err)
	}
}
