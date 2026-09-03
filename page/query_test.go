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
	query := Must(selectSQL, Head(Desc("is_active")), Tie(Asc("id")))

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
}

func TestBuildKeepsRequestedDirectionOverTie(t *testing.T) {
	query := Must(selectSQL, Tie(Asc("id")))

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

	// A field the model does not carry, and one the model carries but Sortable
	// keeps out of reach.
	narrowed := Must(selectSQL, Tie(Asc("id")), Sortable(Cols{"city_eng": "city_eng"}))

	for _, unknown := range []struct {
		query Query
		sort  string
	}{
		{query, "password"},
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
