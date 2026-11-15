package page

import (
	"errors"
	"reflect"
	"testing"
)

func TestSortableFromColumnTag(t *testing.T) {
	type row struct {
		ID       int64  `db:"id"`
		CityEng  string `db:"city_eng"`
		Internal string `db:"-"`
		Tagged   string `db:"tagged"`
		//lint:ignore U1000 Verifies that reflection excludes unexported fields from sortable columns.
		hidden string
	}

	got, err := sortableOf(reflect.TypeFor[row](), columnTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	want := Cols{"id": "id", "city_eng": "city_eng", "tagged": "tagged"}
	assertCols(t, got, want)
}

func TestSortableFromClientTag(t *testing.T) {
	type row struct {
		ID      int64  `db:"id"       json:"id"`
		CityEng string `db:"city_eng" json:"cityEng"`
		Secret  string `db:"secret"   json:"-"`
		Untaged string `db:"untaged"`
	}

	got, err := sortableOf(reflect.TypeFor[row](), "json")
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	// The client's vocabulary maps onto the columns, and a field the client
	// never sees is not a field it can sort by.
	want := Cols{"id": "id", "cityeng": "city_eng"}
	assertCols(t, got, want)
}

func TestSortableFlattensEmbedded(t *testing.T) {
	type tariff struct {
		PerItem float64 `db:"per_item"`
	}

	type row struct {
		tariff

		ID int64 `db:"id"`
	}

	got, err := sortableOf(reflect.TypeFor[row](), columnTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	assertCols(t, got, Cols{"id": "id", "per_item": "per_item"})
}

func TestSortableTaggedEmbeddedIsFlattened(t *testing.T) {
	type point struct {
		X float64 `db:"x"`
	}

	type row struct {
		point `db:"point"`

		ID int64 `db:"id"`
	}

	got, err := sortableOf(reflect.TypeFor[row](), columnTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	assertCols(t, got, Cols{"id": "id", "x": "x"})
}

func TestSortableRejectsNonStruct(t *testing.T) {
	if _, err := sortableOf(reflect.TypeFor[int64](), columnTagKey); !errors.Is(err, ErrUnsortableModel) {
		t.Fatalf("err = %v, want ErrUnsortableModel", err)
	}

	if _, err := sortableOf(nil, columnTagKey); !errors.Is(err, ErrUnsortableModel) {
		t.Fatalf("err = %v, want ErrUnsortableModel", err)
	}
}

func TestSortableIsCached(t *testing.T) {
	type row struct {
		ID int64 `db:"id"`
	}

	first, err := sortableOf(reflect.TypeFor[row](), columnTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	second, err := sortableOf(reflect.TypeFor[row](), columnTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatal("the whitelist was derived twice for one model")
	}
}

func assertCols(t *testing.T, got, want Cols) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("cols = %v, want %v", got, want)
	}

	for field, column := range want {
		if got[field] != column {
			t.Fatalf("cols = %v, want %v", got, want)
		}
	}
}

func TestSortableRejectsAmbiguousTags(t *testing.T) {
	type row struct {
		First  string `db:"first" json:"Name"`
		Second string `db:"second" json:"name"`
	}

	if _, err := sortableOf(reflect.TypeFor[row](), "json"); !errors.Is(err, ErrAmbiguousSortField) {
		t.Fatal(err)
	}

	type embedded struct {
		First string `db:"first" json:"name"`
	}
	type nested struct {
		embedded
		Second string `db:"second" json:"NAME"`
	}

	if _, err := sortableOf(reflect.TypeFor[nested](), "json"); !errors.Is(err, ErrAmbiguousSortField) {
		t.Fatal(err)
	}
}

func TestSortableEmbeddedTagsMatchPGX(t *testing.T) {
	type Point struct {
		X int64 `db:"x" json:"axisX"`
	}
	type ignored struct {
		Point `db:"-" json:"-"`
	}
	type named struct {
		Point Point `db:"point" json:"point"`
	}
	type pointer struct{ *Point }
	type taggedPointer struct {
		*Point `db:"point"`
	}

	for _, tc := range []struct {
		name  string
		model reflect.Type
		tag   string
		want  Cols
	}{
		{
			name:  "ignored value embedding",
			model: reflect.TypeFor[ignored](),
			tag:   "db",
			want:  Cols{"x": "x"},
		},
		{
			name:  "client names on embedded fields",
			model: reflect.TypeFor[ignored](),
			tag:   "json",
			want:  Cols{"axisx": "x"},
		},
		{
			name:  "named struct remains a column",
			model: reflect.TypeFor[named](),
			tag:   "db",
			want:  Cols{"point": "point"},
		},
		{
			name:  "tagged pointer remains a column",
			model: reflect.TypeFor[taggedPointer](),
			tag:   "db",
			want:  Cols{"point": "point"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sortableOf(tc.model, tc.tag)
			if err != nil {
				t.Fatal(err)
			}

			assertCols(t, got, tc.want)
		})
	}

	if _, err := sortableOf(reflect.TypeFor[pointer](), "db"); !errors.Is(err, ErrMissingColumnTag) {
		t.Fatalf("embedded pointer must not expose nested fields: %v", err)
	}
}

func TestWithSortKeyTagUsesExplicitColumn(t *testing.T) {
	type row struct {
		CreatedAt string `json:"createdAt" db:"created_at"`
		Ignored   string `json:"-"`
	}
	cols, err := sortableOf(reflect.TypeFor[row](), "json")
	if err != nil {
		t.Fatal(err)
	}

	if len(cols) != 1 || cols["createdat"] != "created_at" {
		t.Fatalf("columns: %v", cols)
	}
}
