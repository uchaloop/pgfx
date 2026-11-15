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

func build(t *testing.T, query Query, req Request, argc int) Statement {
	t.Helper()

	statement, err := query.BuildStatement(reflect.TypeFor[model](), req, argc)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	return statement
}

func TestMakeQueryRequiresTie(t *testing.T) {
	if _, err := MakeQuery(selectSQL); !errors.Is(err, ErrNoTie) {
		t.Fatalf("err = %v, want ErrNoTie", err)
	}
}

func TestMakeQueryRejectsEmptySQL(t *testing.T) {
	if _, err := MakeQuery("  ;  ", WithTie(MakeAscOrder("id"))); !errors.Is(err, ErrNoSQL) {
		t.Fatalf("err = %v, want ErrNoSQL", err)
	}
}

func TestMakeQueryRejectsInvalidColumn(t *testing.T) {
	if _, err := MakeQuery(selectSQL, WithTie(MakeAscOrder(""))); !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("err = %v, want ErrInvalidColumn", err)
	}

	_, err := MakeQuery(selectSQL, WithTie(MakeAscOrder("id")), WithSortable(Cols{"city": "city\x00eng"}))
	if !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("err = %v, want ErrInvalidColumn", err)
	}
}

func TestMustPanicsOnInvalidQuery(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MakeQueryOrPanic did not panic")
		}
	}()

	MakeQueryOrPanic(selectSQL)
}

func TestBuildRowsStatement(t *testing.T) {
	query := MakeQueryOrPanic(
		selectSQL,
		WithHead(MakeDescOrder("is_active")),
		WithTie(MakeAscOrder("id")),
		WithSortable(Cols{"city_eng": "city_eng"}),
	)

	statement := build(
		t,
		query,
		Request{Page: 4, Limit: 25, Sort: []string{"city_eng"}},
		1,
	)

	wantRows := `SELECT * FROM (` + selectSQL + `) AS pgfx_page ` +
		`ORDER BY "is_active" DESC, "city_eng" ASC, "id" ASC LIMIT $2 OFFSET $3`
	if statement.Rows != wantRows {
		t.Fatalf("rows =\n%s\nwant\n%s", statement.Rows, wantRows)
	}

	if statement.Count != `SELECT COUNT(*) FROM (`+selectSQL+`) AS pgfx_page` {
		t.Fatalf("count = %q", statement.Count)
	}

	if statement.Limit != 25 {
		t.Fatalf("limit = %d, want 25", statement.Limit)
	}

	// Both statements use the same base query; only Rows has page arguments.
	if len(statement.PageArgs) != 2 || statement.PageArgs[0] != uint(25) || statement.PageArgs[1] != uint(75) {
		t.Fatalf("paging args = %v, want [25 75]", statement.PageArgs)
	}
}

func TestBuildTieFollowsPreviousDirection(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")), WithSortable(Cols{"cityEng": "city_eng"}))

	alone := build(
		t,
		query,
		Request{Page: 1, Limit: 10},
		0,
	)
	if !strings.Contains(alone.Rows, `ORDER BY "id" ASC LIMIT`) {
		t.Fatalf("rows = %q, want the tie in its own direction", alone.Rows)
	}

	after := build(
		t,
		query,
		Request{Page: 1, Limit: 10, Sort: []string{"cityEng:desc"}},
		0,
	)
	if !strings.Contains(after.Rows, `ORDER BY "city_eng" DESC, "id" DESC LIMIT`) {
		t.Fatalf("rows = %q, want the tie to follow city_eng", after.Rows)
	}
}

func TestBuildKeepsRequestedDirectionOverTie(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")), WithSortable(Cols{"id": "id"}))

	statement := build(
		t,
		query,
		Request{Page: 1, Limit: 10, Sort: []string{"id:desc"}},
		0,
	)

	// The tie is not appended twice: a client that names the tie column keeps
	// the direction it asked for, and the order stays total either way.
	want := `ORDER BY "id" DESC LIMIT $1 OFFSET $2`
	if !strings.HasSuffix(statement.Rows, want) {
		t.Fatalf("rows = %q, want it to end with %q", statement.Rows, want)
	}
}

func TestBuildAppliesNullsPlacement(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(Order{Column: "id", IsDesc: true, Nulls: NullsLast}))

	statement := build(
		t,
		query,
		Request{Page: 1, Limit: 10},
		0,
	)

	if !strings.Contains(statement.Rows, `ORDER BY "id" DESC NULLS LAST`) {
		t.Fatalf("rows = %q", statement.Rows)
	}
}

func TestBuildQuotesColumns(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder(`we"ird`)))

	statement := build(
		t,
		query,
		Request{Page: 1, Limit: 10},
		0,
	)

	if !strings.Contains(statement.Rows, `ORDER BY "we""ird" ASC`) {
		t.Fatalf("rows = %q", statement.Rows)
	}
}

func TestBuildSortIsCaseInsensitive(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")), WithSortable(Cols{"CityEng": "city_eng"}))

	statement := build(
		t,
		query,
		Request{Page: 1, Limit: 10, Sort: []string{"cityeng:DESC"}},
		0,
	)

	if !strings.Contains(statement.Rows, `ORDER BY "city_eng" DESC`) {
		t.Fatalf("rows = %q", statement.Rows)
	}
}

func TestBuildRejectsUnknownAndMalformedSort(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")))

	// Without a whitelist no field is sortable, not even one the model carries;
	// with one, only the fields it lists.
	narrowed := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")), WithSortable(Cols{"city_eng": "city_eng"}))

	for _, unknown := range []struct {
		query Query
		sort  string
	}{
		{query: query, sort: "city_eng"},
		{query: narrowed, sort: "is_active"},
	} {
		req := Request{Page: 1, Limit: 10, Sort: []string{unknown.sort}}

		_, err := unknown.query.BuildStatement(reflect.TypeFor[model](), req, 0)
		if !errors.Is(err, ErrUnknownSortField) {
			t.Fatalf("sort %q: err = %v, want ErrUnknownSortField", unknown.sort, err)
		}
	}

	for _, sort := range []string{"id:up", ":desc", "  "} {
		_, err := query.BuildStatement(reflect.TypeFor[model](), Request{Page: 1, Limit: 10, Sort: []string{sort}}, 0)
		if !errors.Is(err, ErrInvalidSort) {
			t.Fatalf("sort %q: err = %v, want ErrInvalidSort", sort, err)
		}
	}
}

func TestBuildRejectsInvalidRequest(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")))

	for name, req := range map[string]Request{
		"zero number":  {Page: 0, Limit: 10},
		"zero size":    {Page: 1, Limit: 0},
		"offset wraps": {Page: 1 << 62, Limit: 1 << 12},
		// LIMIT must fit PostgreSQL bigint.
		"size too large": {Page: 1, Limit: 1 << 63},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := query.BuildStatement(reflect.TypeFor[model](), req, 0); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestBuildWithoutSortNeedsNoModel(t *testing.T) {
	query := MakeQueryOrPanic(selectSQL, WithTie(MakeAscOrder("id")))

	// Nothing to whitelist means nothing to reflect over: a scalar page, or a
	// model that carries no tags at all, still paginates.
	if _, err := query.BuildStatement(nil, Request{Page: 1, Limit: 10}, 0); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestBuildRejectsUninitializedQuery(t *testing.T) {
	if _, err := (Query{}).BuildStatement(nil, Request{Page: 1, Limit: 20}, 0); !errors.Is(err, ErrNoSQL) {
		t.Fatalf("zero query: %v", err)
	}
}

func TestWithSortKeyTagRejectsAbsentColumnTag(t *testing.T) {
	type row struct {
		ID        int64  `db:"id"`
		CreatedAt string `json:"createdAt"`
	}
	q := MakeQueryOrPanic("SELECT id, created_at FROM items", WithTie(MakeAscOrder("id")), WithSortKeyTag("json"))
	if _, err := q.BuildStatement(reflect.TypeFor[row](), Request{Page: 1, Limit: 10, Sort: []string{"createdAt"}}, 0); !errors.Is(err, ErrMissingColumnTag) {
		t.Fatal("missing db tag must fail before SQL execution")
	}
}

func TestBuildAllowsBigintLimit(t *testing.T) {
	query := MakeQueryOrPanic("SELECT id FROM items", WithTie(MakeAscOrder("id")))
	statement, err := query.BuildStatement(nil, Request{Page: 1, Limit: 1<<63 - 1}, 0)
	if err != nil || statement.PageArgs[0] != uint(1<<63-1) {
		t.Fatalf("statement=%+v err=%v", statement, err)
	}
}

func TestBuildPreservesFinalLineComment(t *testing.T) {
	query := MakeQueryOrPanic("SELECT id FROM items WHERE tenant=$1 -- tenant filter", WithTie(MakeAscOrder("id")))
	statement, err := query.BuildStatement(nil, Request{Page: 1, Limit: 2}, 1)
	if err != nil {
		t.Fatal(err)
	}

	for _, sql := range []string{statement.Rows, statement.Count} {
		if !strings.Contains(sql, "-- tenant filter\n) AS pgfx_page") {
			t.Fatalf("wrapper consumed by comment: %s", sql)
		}
	}
}
