package pgfx

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeQuerier struct {
	rows pgx.Rows
	err  error
}

func (q fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return q.rows, q.err
}

var _ pgx.Rows = (*fakeRows)(nil)

type fakeRows struct {
	typeMap  *pgtype.Map
	fields   []pgconn.FieldDescription
	values   [][]any
	index    int
	current  []any
	err      error
	scanErr  error
	isClosed bool
}

func makeFakeRows(columns []string, values ...[]any) *fakeRows {
	fields := make([]pgconn.FieldDescription, len(columns))

	for i, column := range columns {
		fields[i].Name = column
	}

	return &fakeRows{fields: fields, values: values}
}

func (r *fakeRows) Close() {
	r.isClosed = true
}

func (r *fakeRows) Err() error {
	if !r.isClosed {
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
		if dest[i] == nil {
			continue
		}

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

func (r *fakeRows) TypeMap() *pgtype.Map {
	if r.typeMap == nil {
		r.typeMap = pgtype.NewMap()
	}

	return r.typeMap
}

func (*fakeRows) Conn() *pgx.Conn {
	return nil
}

func TestCollectManyStructRows(t *testing.T) {
	type order struct {
		ID     int64 `db:"id"`
		Amount int64 `db:"amount"`
	}

	rows := makeFakeRows(
		[]string{"id", "amount"},
		[]any{int64(1), int64(100)},
		[]any{int64(2), int64(200)},
	)

	got, err := collectMany(
		context.Background(),
		fakeQuerier{rows: rows},
		pgx.RowToStructByNameLax[order],
		"select",
	)
	if err != nil {
		t.Fatalf("collectMany: %v", err)
	}

	want := []order{{ID: 1, Amount: 100}, {ID: 2, Amount: 200}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectMany = %#v, want %#v", got, want)
	}

	if !rows.isClosed {
		t.Fatal("collectMany did not close rows")
	}
}

func TestCollectOneStructRow(t *testing.T) {
	type order struct {
		ID int64 `db:"id"`
	}

	rows := makeFakeRows([]string{"id"}, []any{int64(7)})
	got, err := collectOne(
		context.Background(),
		fakeQuerier{rows: rows},
		pgx.RowToStructByNameLax[order],
		"select",
	)
	if err != nil {
		t.Fatalf("collectOne: %v", err)
	}

	if got.ID != 7 {
		t.Fatalf("collectOne ID = %d, want 7", got.ID)
	}

	if !rows.isClosed {
		t.Fatal("collectOne did not close rows")
	}
}

func TestCollectManyValues(t *testing.T) {
	rows := makeFakeRows(
		[]string{"id"},
		[]any{int64(1)},
		[]any{int64(2)},
	)
	got, err := collectMany(
		context.Background(),
		fakeQuerier{rows: rows},
		pgx.RowTo[int64],
		"select",
	)
	if err != nil {
		t.Fatalf("collectMany: %v", err)
	}

	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("collectMany = %v, want [1 2]", got)
	}
}

func TestCollectOneValue(t *testing.T) {
	rows := makeFakeRows([]string{"count"}, []any{int64(3)})
	got, err := collectOne(
		context.Background(),
		fakeQuerier{rows: rows},
		pgx.RowTo[int64],
		"select",
	)
	if err != nil {
		t.Fatalf("collectOne: %v", err)
	}

	if got != 3 {
		t.Fatalf("collectOne = %d, want 3", got)
	}
}

func TestCollectOneErrors(t *testing.T) {
	t.Run("no rows", func(t *testing.T) {
		_, err := collectOne(
			context.Background(),
			fakeQuerier{rows: makeFakeRows([]string{"id"})},
			pgx.RowTo[int64],
			"select",
		)
		// pgx.ErrNoRows wraps sql.ErrNoRows, so a caller matches whichever
		// sentinel it already checks for. Both are pinned here: mapping one to
		// the other would silently take the other away.
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("collectOne error = %v, want sql.ErrNoRows", err)
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("collectOne error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("too many rows", func(t *testing.T) {
		rows := makeFakeRows(
			[]string{"id"},
			[]any{int64(1)},
			[]any{int64(2)},
		)
		_, err := collectOne(
			context.Background(),
			fakeQuerier{rows: rows},
			pgx.RowTo[int64],
			"select",
		)
		if !errors.Is(err, pgx.ErrTooManyRows) {
			t.Fatalf("collectOne error = %v, want pgx.ErrTooManyRows", err)
		}
	})
}

func TestCollectPropagatesQueryError(t *testing.T) {
	errExpected := errors.New("query failed")
	got, err := collectMany(
		context.Background(),
		fakeQuerier{err: errExpected},
		pgx.RowTo[int64],
		"select",
	)
	if got != nil || err != errExpected {
		t.Fatalf("collectMany = %v, %v; want nil and original error", got, err)
	}
}

func TestFetchIntoGrowsAndClosesOnError(t *testing.T) {
	rows := makeFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)})
	got, err := collectInto(
		context.Background(),
		fakeQuerier{rows: rows},
		make([]int64, 0, 1),
		pgx.RowTo[int64],
		"query",
	)
	if err != nil || len(got) != 2 || !rows.isClosed {
		t.Fatalf("got=%v err=%v", got, err)
	}

	bad := makeFakeRows([]string{"id"}, []any{int64(1)}, []any{"invalid"})
	buffer := make([]int64, 0, 2)
	got, err = collectInto(
		context.Background(),
		fakeQuerier{rows: bad},
		buffer,
		pgx.RowTo[int64],
		"query",
	)
	if err == nil || got != nil || !bad.isClosed || buffer[:1][0] != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestCollectClosesRowsOnScanError(t *testing.T) {
	errExpected := errors.New("scan failed")
	rows := makeFakeRows([]string{"id"}, []any{int64(1)})
	rows.scanErr = errExpected

	_, err := collectMany(
		context.Background(),
		fakeQuerier{rows: rows},
		pgx.RowTo[int64],
		"select",
	)
	if !errors.Is(err, errExpected) {
		t.Fatalf("collectMany error = %v, want %v", err, errExpected)
	}

	if !rows.isClosed {
		t.Fatal("collectMany did not close rows after a scan error")
	}
}

type warehouse struct {
	ID      int64  `db:"id"      json:"id"`
	CityEng string `db:"city_eng" json:"cityEng"`
}

func makeWarehouseRows(ids ...int64) *fakeRows {
	values := make([][]any, 0, len(ids))

	for _, id := range ids {
		values = append(values, []any{id, "Istanbul"})
	}

	return makeFakeRows([]string{"id", "city_eng"}, values...)
}

func TestCollectManyEmptyResult(t *testing.T) {
	rows := makeFakeRows([]string{"id"})
	got, err := collectMany(
		context.Background(),
		fakeQuerier{rows: rows},
		pgx.RowTo[int64],
		"select",
	)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("collectMany = %#v, %v; want non-nil empty slice", got, err)
	}

	if !rows.isClosed {
		t.Fatal("empty rows were not closed")
	}
}

func TestCollectManyDiscardsPartialResult(t *testing.T) {
	for _, stage := range []string{"scan", "iteration"} {
		t.Run(stage, func(t *testing.T) {
			errExpected := errors.New("row failed")
			rows := makeFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)})
			calls := 0
			scan := func(row pgx.CollectableRow) (int64, error) {
				calls++
				if calls == 2 {
					if stage == "scan" {
						return 0, errExpected
					}
					// Surface the iteration error after successful decoding.
					rows.err = errExpected
				}

				return pgx.RowTo[int64](row)
			}
			got, err := collectMany(
				context.Background(),
				fakeQuerier{rows: rows},
				scan,
				"select",
			)
			if got != nil || err != errExpected {
				t.Fatalf("collectMany = %v, %v; want nil and original error", got, err)
			}

			if calls != 2 || !rows.isClosed {
				t.Fatalf("scan calls = %d, closed = %v", calls, rows.isClosed)
			}
		})
	}
}

type queryProbe func(context.Context, string, ...any) (pgx.Rows, error)

func (q queryProbe) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q(ctx, sql, args...)
}

func TestCollectManyPreservesQueryInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sql = "select id where tenant = $1 and active = $2"
	args := []any{"tenant", true}
	rows := makeFakeRows([]string{"id"}, []any{int64(7)})
	calls := 0
	q := queryProbe(func(gotCtx context.Context, gotSQL string, gotArgs ...any) (pgx.Rows, error) {
		calls++
		if gotCtx != ctx || gotSQL != sql || !reflect.DeepEqual(gotArgs, args) {
			t.Fatal("query inputs changed")
		}

		return rows, nil
	})
	got, err := collectMany(
		ctx,
		q,
		func(row pgx.CollectableRow) (int64, error) {
			value, err := pgx.RowTo[int64](row)
			if err != nil {
				return 0, err
			}

			return value * 2, nil
		},
		sql,
		args...,
	)
	if err != nil || !reflect.DeepEqual(got, []int64{14}) {
		t.Fatalf("collectMany = %v, %v; want [14]", got, err)
	}

	if calls != 1 || !rows.isClosed {
		t.Fatalf("query calls = %d, closed = %v", calls, rows.isClosed)
	}
}
