package page

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type model struct {
	ID       int64  `db:"id"        json:"id"`
	CityEng  string `db:"city_eng"  json:"cityEng"`
	IsActive bool   `db:"is_active" json:"isActive"`
}

const selectSQL = "SELECT id, city_eng, is_active FROM warehouses WHERE country = $1"

func build(t *testing.T, query Query, req Request, argc int) Statements {
	t.Helper()

	statements, err := query.Build(reflect.TypeFor[model](), req, argc)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	return statements
}

func TestMakeRequiresTie(t *testing.T) {
	if _, err := Make(selectSQL); !errors.Is(err, ErrNoTie) {
		t.Fatalf("err = %v, want ErrNoTie", err)
	}
}

func TestMakeRejectsEmptySQL(t *testing.T) {
	if _, err := Make("  ;  ", Tie(Asc("id"))); !errors.Is(err, ErrNoSQL) {
		t.Fatalf("err = %v, want ErrNoSQL", err)
	}
}

func TestMakeRejectsInvalidColumn(t *testing.T) {
	if _, err := Make(selectSQL, Tie(Asc(""))); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("err = %v, want ErrInvalidColumn", err)
	}

	_, err := Make(selectSQL, Tie(Asc("id")), Sortable(Cols{"city": "city\x00eng"}))
	if !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("err = %v, want ErrInvalidColumn", err)
	}
}

func TestMustPanicsOnInvalidQuery(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Must did not panic")
		}
	}()

	Must(selectSQL)
}

func TestBuildStatements(t *testing.T) {
	query := Must(selectSQL, Head(Desc("is_active")), Tie(Asc("id")), Sortable(Cols{"city_eng": "city_eng"}))

	statements := build(t, query, Request{Number: 4, Size: 25, Sort: []string{"city_eng"}}, 1)

	wantRows := `SELECT * FROM (` + selectSQL + `) AS pgfx_page ` +
		`ORDER BY "is_active" DESC, "city_eng" ASC, "id" ASC LIMIT $2 OFFSET $3`
	if statements.Rows != wantRows {
		t.Fatalf("rows =\n%s\nwant\n%s", statements.Rows, wantRows)
	}

	wantCount := `SELECT count(*) FROM (` + selectSQL + `) AS pgfx_page`
	if statements.Count != wantCount {
		t.Fatalf("count =\n%s\nwant\n%s", statements.Count, wantCount)
	}

	if statements.Limit != 25 || statements.Offset != 75 {
		t.Fatalf("limit = %d, offset = %d, want 25 and 75", statements.Limit, statements.Offset)
	}

	// The rows statement reads one row past the page.
	if len(statements.PagingArgs) != 2 || statements.PagingArgs[0] != uint(26) || statements.PagingArgs[1] != uint(75) {
		t.Fatalf("paging args = %v, want [26 75]", statements.PagingArgs)
	}
}

func TestBuildTieFollowsPrecedingDirection(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")), Sortable(Cols{"cityEng": "city_eng"}))

	alone := build(t, query, Request{Number: 1, Size: 10}, 0)
	if !strings.Contains(alone.Rows, `ORDER BY "id" ASC LIMIT`) {
		t.Fatalf("rows = %q, want the tie in its own direction", alone.Rows)
	}

	after := build(t, query, Request{Number: 1, Size: 10, Sort: []string{"cityEng:desc"}}, 0)
	if !strings.Contains(after.Rows, `ORDER BY "city_eng" DESC, "id" DESC LIMIT`) {
		t.Fatalf("rows = %q, want the tie to follow city_eng", after.Rows)
	}
}

func TestBuildKeepsRequestedDirectionOverTie(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")), Sortable(Cols{"id": "id"}))

	statements := build(t, query, Request{Number: 1, Size: 10, Sort: []string{"id:desc"}}, 0)

	// The tie is not appended twice: a client that names the tie column keeps
	// the direction it asked for, and the order stays total either way.
	want := `ORDER BY "id" DESC LIMIT $1 OFFSET $2`
	if !strings.HasSuffix(statements.Rows, want) {
		t.Fatalf("rows = %q, want it to end with %q", statements.Rows, want)
	}
}

func TestBuildAppliesNullsPlacement(t *testing.T) {
	query := Must(selectSQL, Tie(Order{Column: "id", Desc: true, Nulls: NullsLast}))

	statements := build(t, query, Request{Number: 1, Size: 10}, 0)

	if !strings.Contains(statements.Rows, `ORDER BY "id" DESC NULLS LAST`) {
		t.Fatalf("rows = %q", statements.Rows)
	}
}

func TestBuildQuotesColumns(t *testing.T) {
	query := Must(selectSQL, Tie(Asc(`we"ird`)))

	statements := build(t, query, Request{Number: 1, Size: 10}, 0)

	if !strings.Contains(statements.Rows, `ORDER BY "we""ird" ASC`) {
		t.Fatalf("rows = %q", statements.Rows)
	}
}

func TestBuildCountSQL(t *testing.T) {
	query := Must(selectSQL,
		Tie(Asc("id")),
		CountSQL("SELECT 1 FROM warehouses WHERE country = $1;"),
	)

	statements := build(t, query, Request{Number: 1, Size: 10}, 1)

	want := "SELECT count(*) FROM (SELECT 1 FROM warehouses WHERE country = $1) AS pgfx_page"
	if statements.Count != want {
		t.Fatalf("count = %q, want %q", statements.Count, want)
	}
}

func TestBuildSortIsCaseInsensitive(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")), Sortable(Cols{"CityEng": "city_eng"}))

	statements := build(t, query, Request{Number: 1, Size: 10, Sort: []string{"cityeng:DESC"}}, 0)

	if !strings.Contains(statements.Rows, `ORDER BY "city_eng" DESC`) {
		t.Fatalf("rows = %q", statements.Rows)
	}
}

func TestBuildRejectsUnknownAndMalformedSort(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")))

	// Without a whitelist no field is sortable, not even one the model carries;
	// with one, only the fields it lists.
	narrowed := Must(selectSQL, Tie(Asc("id")), Sortable(Cols{"city_eng": "city_eng"}))

	for _, unknown := range []struct {
		query Query
		sort  string
	}{
		{query, "city_eng"},
		{narrowed, "is_active"},
	} {
		req := Request{Number: 1, Size: 10, Sort: []string{unknown.sort}}

		_, err := unknown.query.Build(reflect.TypeFor[model](), req, 0)
		if !errors.Is(err, ErrUnknownSortField) {
			t.Fatalf("sort %q: err = %v, want ErrUnknownSortField", unknown.sort, err)
		}
	}

	for _, sort := range []string{"id:up", ":desc", "  "} {
		_, err := query.Build(reflect.TypeFor[model](), Request{Number: 1, Size: 10, Sort: []string{sort}}, 0)
		if !errors.Is(err, ErrInvalidSort) {
			t.Fatalf("sort %q: err = %v, want ErrInvalidSort", sort, err)
		}
	}
}

func TestBuildRejectsInvalidRequest(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")))

	for name, req := range map[string]Request{
		"zero number":  {Number: 0, Size: 10},
		"zero size":    {Number: 1, Size: 0},
		"offset wraps": {Number: 1 << 62, Size: 1 << 12},
		// The extra row past the page would not fit LIMIT's bigint.
		"size too large": {Number: 1, Size: 1<<63 - 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := query.Build(reflect.TypeFor[model](), req, 0); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestBuildWithoutSortNeedsNoModel(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")))

	// Nothing to whitelist means nothing to reflect over: a scalar page, or a
	// model that carries no tags at all, still paginates.
	if _, err := query.Build(nil, Request{Number: 1, Size: 10}, 0); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestBuildRejectsUninitializedQuery(t *testing.T) {
	if _, err := (Query{}).Build(nil, Request{Number: 1, Size: 20}, 0); !errors.Is(err, ErrNoSQL) {
		t.Fatalf("zero query: %v", err)
	}
}

func TestCursorMixedUsesDisjointBranches(t *testing.T) {
	q := Must("SELECT rank,name,id FROM items", Head(Desc("rank")), Tie(Asc("id")), Sortable(Cols{"name": "name"}))
	req := CursorRequest{Size: 20, Sort: []string{"name"}}

	st, err := q.BuildAfter(nil, req)
	if err != nil {
		t.Fatal(err)
	}

	cursor, err := st.Cursor([]any{int64(10), "b", int64(42)})
	if err != nil {
		t.Fatal(err)
	}

	req.After = cursor
	st, err = q.BuildAfter(nil, req)
	if err != nil {
		t.Fatal(err)
	}

	// rank DESC followed by name ASC cannot be one row comparison.
	if !strings.Contains(st.Rows, "UNION ALL") || strings.Contains(st.Rows, " OR ") {
		t.Fatal(st.Rows)
	}

	if !strings.Contains(st.Rows, `"rank" = $1 AND "name" > $2`) {
		t.Fatal(st.Rows)
	}
}

func TestCursorTieFollowingDirectionUsesTuple(t *testing.T) {
	q := Must("SELECT rank,id FROM items", Head(Desc("rank")), Tie(Asc("id")))

	st, err := q.BuildAfter(nil, CursorRequest{Size: 20})
	if err != nil {
		t.Fatal(err)
	}

	cursor, err := st.Cursor([]any{int64(10), int64(42)})
	if err != nil {
		t.Fatal(err)
	}

	st, err = q.BuildAfter(nil, CursorRequest{Size: 20, After: cursor})
	if err != nil {
		t.Fatal(err)
	}

	// The tie follows rank DESC, so one row comparison seeks an index on
	// (rank, id) without UNION branches.
	if strings.Contains(st.Rows, "UNION ALL") || !strings.Contains(st.Rows, `("rank", "id") < ($1, $2)`) {
		t.Fatal(st.Rows)
	}
}

func TestBuildRowsDoesNotBuildCount(t *testing.T) {
	q := Must(selectSQL, Tie(Asc("id")), CountSQL("SELECT expensive_count_source"))
	st, err := q.BuildRows(nil, Request{Number: 2, Size: 20}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != "" {
		t.Fatal(st.Count)
	}
	count, err := q.BuildCount()
	if err != nil || !strings.Contains(count, "expensive_count_source") {
		t.Fatalf("%s %v", count, err)
	}
}

func BenchmarkPaginationBuild(b *testing.B) {
	for _, mode := range []string{"offset", "first", "after", "mixed", "null"} {
		b.Run(
			mode,
			func(b *testing.B) {
				direction := Asc("rank")
				if mode == "mixed" {
					direction = Desc("rank")
				}
				q := Must("SELECT id, rank, meta FROM items WHERE tenant=$1", Head(direction), Tie(Asc("id")))
				st, err := q.BuildAfter(nil, CursorRequest{Size: 20}, "tenant")
				if err != nil {
					b.Fatal(err)
				}
				keys := []any{int64(5), int64(900000)}
				if mode == "null" {
					keys[0] = nil
				}
				token, err := st.Cursor(keys)
				if err != nil {
					b.Fatal(err)
				}
				req := CursorRequest{Size: 20}
				if mode != "first" {
					req.After = token
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if mode == "offset" {
						if _, err := q.BuildRows(nil, Request{Number: 45001, Size: 20}, 1); err != nil {
							b.Fatal(err)
						}
					} else if _, err := q.BuildAfter(nil, req, "tenant"); err != nil {
						b.Fatal(err)
					}
				}
			},
		)
	}
}

func TestCursorBuildDoesNotMutateFilterArgs(t *testing.T) {
	backing := []any{"tenant", "untouched", "untouched", "untouched"}
	q := Must("SELECT id FROM items WHERE tenant=$1", Tie(Asc("id")))
	st, err := q.BuildAfter(nil, CursorRequest{Size: 2}, backing[:1]...)
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.Cursor([]any{int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.BuildAfter(nil, CursorRequest{Size: 2, After: token}, backing[:1]...); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backing, []any{"tenant", "untouched", "untouched", "untouched"}) {
		t.Fatal(backing)
	}
}
