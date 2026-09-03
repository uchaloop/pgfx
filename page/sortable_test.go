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
		NoTag    string
		hidden   string // an unexported field is not a column
	}

	got, err := sortableOf(reflect.TypeFor[row](), defaultTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	want := Cols{"id": "id", "city_eng": "city_eng", "notag": "notag"}
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

	got, err := sortableOf(reflect.TypeFor[row](), defaultTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	assertCols(t, got, Cols{"id": "id", "per_item": "per_item"})
}

func TestSortableTaggedEmbeddedIsOneColumn(t *testing.T) {
	type point struct {
		X float64 `db:"x"`
	}

	type row struct {
		point `db:"point"`

		ID int64 `db:"id"`
	}

	got, err := sortableOf(reflect.TypeFor[row](), defaultTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	assertCols(t, got, Cols{"id": "id", "point": "point"})
}

func TestSortableRejectsNonStruct(t *testing.T) {
	if _, err := sortableOf(reflect.TypeFor[int64](), defaultTagKey); !errors.Is(err, ErrUnsortableModel) {
		t.Fatalf("err = %v, want ErrUnsortableModel", err)
	}

	if _, err := sortableOf(nil, defaultTagKey); !errors.Is(err, ErrUnsortableModel) {
		t.Fatalf("err = %v, want ErrUnsortableModel", err)
	}
}

func TestSortableIsCached(t *testing.T) {
	type row struct {
		ID int64 `db:"id"`
	}

	first, err := sortableOf(reflect.TypeFor[row](), defaultTagKey)
	if err != nil {
		t.Fatalf("sortableOf: %v", err)
	}

	second, err := sortableOf(reflect.TypeFor[row](), defaultTagKey)
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
