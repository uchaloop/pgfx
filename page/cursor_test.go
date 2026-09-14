package page

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTripAndBinding(t *testing.T) {
	q := Must("SELECT id FROM items WHERE tenant=$1", Tie(Asc("id")))
	first, err := q.BuildAfter(nil, CursorRequest{Size: 20}, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	token, err := first.Cursor([]any{int64(math.MaxInt64)})
	if err != nil {
		t.Fatal(err)
	}
	next, err := q.BuildAfter(nil, CursorRequest{Size: 5, After: token}, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Args, []any{"tenant-a", int64(math.MaxInt64), uint(6)}) {
		t.Fatal(next.Args)
	}
	if strings.Contains(next.Rows, "OFFSET") || strings.Contains(next.Rows, "count(") || strings.Contains(next.Rows, "row_number") {
		t.Fatal(next.Rows)
	}
	for _, args := range [][]any{{"tenant-b"}, {1}} {
		if _, err := q.BuildAfter(nil, CursorRequest{Size: 20, After: token}, args...); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal(err)
		}
	}
	changed := Must("SELECT id FROM other WHERE tenant=$1", Tie(Asc("id")))
	if _, err := changed.BuildAfter(nil, CursorRequest{Size: 20, After: token}, "tenant-a"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal(err)
	}
	changed = Must("SELECT id FROM items WHERE tenant=$1", Tie(Desc("id")))
	if _, err := changed.BuildAfter(nil, CursorRequest{Size: 20, After: token}, "tenant-a"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal(err)
	}
}

func TestCursorKeyTypes(t *testing.T) {
	values := []any{nil, int64(math.MaxInt64), uint64(math.MaxUint64), int32(-42), true, "a'b", []byte{1, 2}, [16]byte{1, 2}, time.Date(2026, 9, 10, 12, 0, 0, 123456000, time.UTC), float64(1.25)}
	st := CursorStatement{scope: "test", Orders: make([]Order, len(values))}
	token, err := st.Cursor(values)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCursor(token, "test", len(values))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, decoded) {
		t.Fatalf("%#v", decoded)
	}
	for _, bad := range []any{map[string]any{}, math.NaN(), make(chan int)} {
		if _, err := encodeValue(bad); !errors.Is(err, ErrCursorValue) {
			t.Fatal(err)
		}
	}
}

func TestCursorValidation(t *testing.T) {
	q := Must("SELECT id FROM items", Tie(Asc("id")))
	for _, size := range []uint{0, uint(math.MaxInt64)} {
		if _, err := q.BuildAfter(nil, CursorRequest{Size: size}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
	for _, token := range []string{"broken", "e30", strings.Repeat("x", maxCursorBytes+1)} {
		if _, err := q.BuildAfter(nil, CursorRequest{Size: 20, After: token}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal(err)
		}
	}
	if _, err := q.BuildAfter(nil, CursorRequest{Size: 20}, make(chan int)); !errors.Is(err, ErrCursorValue) {
		t.Fatal(err)
	}
	if _, err := Make("SELECT id", Tie(Order{Column: "id", Nulls: 99})); !errors.Is(err, ErrInvalidNulls) {
		t.Fatal(err)
	}
	if _, err := Make("SELECT id", Tie(Asc("id")), Sortable(Cols{"Name": "id", "name": "other"})); !errors.Is(err, ErrAmbiguousSortField) {
		t.Fatal(err)
	}
}

func BenchmarkCursorCodec(b *testing.B) {
	st := CursorStatement{scope: "benchmark", Orders: make([]Order, 3)}
	keys := []any{int64(900000), "product", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	token, err := st.Cursor(keys)
	if err != nil {
		b.Fatal(err)
	}
	b.Run(
		"encode",
		func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := st.Cursor(keys); err != nil {
					b.Fatal(err)
				}
			}
		},
	)
	b.Run(
		"decode",
		func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := decodeCursor(token, "benchmark", 3); err != nil {
					b.Fatal(err)
				}
			}
		},
	)
}
