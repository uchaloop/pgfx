package pgfx

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeQuerier struct {
	rows pgx.Rows
	err  error
}

func (q fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return q.rows, q.err
}

type fakeRows struct {
	fields  []pgconn.FieldDescription
	values  [][]any
	index   int
	current []any
	err     error
	scanErr error
	closed  bool
}

func newFakeRows(columns []string, values ...[]any) *fakeRows {
	fields := make([]pgconn.FieldDescription, len(columns))
	for i, column := range columns {
		fields[i].Name = column
	}

	return &fakeRows{fields: fields, values: values}
}

func (r *fakeRows) Close() {
	r.closed = true
}

func (r *fakeRows) Err() error {
	if !r.closed {
		return nil
	}

	return r.err
}

func (*fakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return r.fields
}

func (r *fakeRows) Next() bool {
	if r.index >= len(r.values) || r.err != nil {
		r.Close()

		return false
	}

	r.current = r.values[r.index]
	r.index++

	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		r.Close()

		return r.scanErr
	}
	if len(dest) != len(r.current) {
		r.Close()

		return errors.New("destination count does not match row")
	}

	for i, value := range r.current {
		target := reflect.ValueOf(dest[i])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			r.Close()

			return errors.New("scan destination is not a pointer")
		}
		if value == nil {
			target.Elem().SetZero()

			continue
		}

		source := reflect.ValueOf(value)
		if source.Type().AssignableTo(target.Elem().Type()) {
			target.Elem().Set(source)
		} else if source.Type().ConvertibleTo(target.Elem().Type()) {
			target.Elem().Set(source.Convert(target.Elem().Type()))
		} else {
			r.Close()

			return errors.New("row value cannot be assigned to destination")
		}
	}

	return nil
}

func (r *fakeRows) Values() ([]any, error) {
	return r.current, nil
}

func (*fakeRows) RawValues() [][]byte {
	return nil
}

func (*fakeRows) Conn() *pgx.Conn {
	return nil
}

func TestFetchRows(t *testing.T) {
	type order struct {
		ID     int64 `db:"id"`
		Amount int64 `db:"amount"`
	}

	rows := newFakeRows(
		[]string{"id", "amount"},
		[]any{int64(1), int64(100)},
		[]any{int64(2), int64(200)},
	)

	got, err := FetchRows[order](context.Background(), fakeQuerier{rows: rows}, "select")
	if err != nil {
		t.Fatalf("FetchRows: %v", err)
	}
	want := []order{{ID: 1, Amount: 100}, {ID: 2, Amount: 200}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FetchRows = %#v, want %#v", got, want)
	}
	if !rows.closed {
		t.Fatal("FetchRows did not close rows")
	}
}

func TestFetchRow(t *testing.T) {
	type order struct {
		ID int64 `db:"id"`
	}

	rows := newFakeRows([]string{"id"}, []any{int64(7)})
	got, err := FetchRow[order](context.Background(), fakeQuerier{rows: rows}, "select")
	if err != nil {
		t.Fatalf("FetchRow: %v", err)
	}
	if got.ID != 7 {
		t.Fatalf("FetchRow ID = %d, want 7", got.ID)
	}
	if !rows.closed {
		t.Fatal("FetchRow did not close rows")
	}
}

func TestFetchValues(t *testing.T) {
	rows := newFakeRows(
		[]string{"id"},
		[]any{int64(1)},
		[]any{int64(2)},
	)
	got, err := FetchValues[int64](context.Background(), fakeQuerier{rows: rows}, "select")
	if err != nil {
		t.Fatalf("FetchValues: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("FetchValues = %v, want [1 2]", got)
	}
}

func TestFetchValue(t *testing.T) {
	rows := newFakeRows([]string{"count"}, []any{int64(3)})
	got, err := FetchValue[int64](context.Background(), fakeQuerier{rows: rows}, "select")
	if err != nil {
		t.Fatalf("FetchValue: %v", err)
	}
	if got != 3 {
		t.Fatalf("FetchValue = %d, want 3", got)
	}
}

func TestFetchOneErrors(t *testing.T) {
	t.Run("no rows", func(t *testing.T) {
		_, err := FetchValue[int64](
			context.Background(),
			fakeQuerier{rows: newFakeRows([]string{"id"})},
			"select",
		)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("FetchValue error = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("too many rows", func(t *testing.T) {
		rows := newFakeRows(
			[]string{"id"},
			[]any{int64(1)},
			[]any{int64(2)},
		)
		_, err := FetchValue[int64](context.Background(), fakeQuerier{rows: rows}, "select")
		if !errors.Is(err, pgx.ErrTooManyRows) {
			t.Fatalf("FetchValue error = %v, want pgx.ErrTooManyRows", err)
		}
	})
}

func TestFetchPropagatesQueryError(t *testing.T) {
	want := errors.New("query failed")
	_, err := FetchValues[int64](
		context.Background(),
		fakeQuerier{err: want},
		"select",
	)
	if !errors.Is(err, want) {
		t.Fatalf("FetchValues error = %v, want %v", err, want)
	}
}

func TestFetchClosesRowsOnScanError(t *testing.T) {
	want := errors.New("scan failed")
	rows := newFakeRows([]string{"id"}, []any{int64(1)})
	rows.scanErr = want

	_, err := FetchValues[int64](context.Background(), fakeQuerier{rows: rows}, "select")
	if !errors.Is(err, want) {
		t.Fatalf("FetchValues error = %v, want %v", err, want)
	}
	if !rows.closed {
		t.Fatal("FetchValues did not close rows after a scan error")
	}
}
