package pgfx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uchaloop/pgfx/page"
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

func (*fakeRows) Conn() *pgx.Conn {
	return nil
}

func TestCollectManyStructRows(t *testing.T) {
	type order struct {
		ID     int64 `db:"id"`
		Amount int64 `db:"amount"`
	}

	rows := newFakeRows(
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
	if !rows.closed {
		t.Fatal("collectMany did not close rows")
	}
}

func TestCollectOneStructRow(t *testing.T) {
	type order struct {
		ID int64 `db:"id"`
	}

	rows := newFakeRows([]string{"id"}, []any{int64(7)})
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
	if !rows.closed {
		t.Fatal("collectOne did not close rows")
	}
}

func TestCollectManyValues(t *testing.T) {
	rows := newFakeRows(
		[]string{"id"},
		[]any{int64(1)},
		[]any{int64(2)},
	)
	got, err := collectMany(context.Background(), fakeQuerier{rows: rows}, pgx.RowTo[int64], "select")
	if err != nil {
		t.Fatalf("collectMany: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("collectMany = %v, want [1 2]", got)
	}
}

func TestCollectOneValue(t *testing.T) {
	rows := newFakeRows([]string{"count"}, []any{int64(3)})
	got, err := collectOne(context.Background(), fakeQuerier{rows: rows}, pgx.RowTo[int64], "select")
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
			fakeQuerier{rows: newFakeRows([]string{"id"})},
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
		rows := newFakeRows(
			[]string{"id"},
			[]any{int64(1)},
			[]any{int64(2)},
		)
		_, err := collectOne(context.Background(), fakeQuerier{rows: rows}, pgx.RowTo[int64], "select")
		if !errors.Is(err, pgx.ErrTooManyRows) {
			t.Fatalf("collectOne error = %v, want pgx.ErrTooManyRows", err)
		}
	})
}

func TestCollectPropagatesQueryError(t *testing.T) {
	want := errors.New("query failed")
	_, err := collectMany(
		context.Background(),
		fakeQuerier{err: want},
		pgx.RowTo[int64],
		"select",
	)
	if !errors.Is(err, want) {
		t.Fatalf("collectMany error = %v, want %v", err, want)
	}
}

func TestCollectClosesRowsOnScanError(t *testing.T) {
	want := errors.New("scan failed")
	rows := newFakeRows([]string{"id"}, []any{int64(1)})
	rows.scanErr = want

	_, err := collectMany(context.Background(), fakeQuerier{rows: rows}, pgx.RowTo[int64], "select")
	if !errors.Is(err, want) {
		t.Fatalf("collectMany error = %v, want %v", err, want)
	}
	if !rows.closed {
		t.Fatal("collectMany did not close rows after a scan error")
	}
}

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

func TestCollectPageRowsNeverCounts(t *testing.T) {

	for _, n := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			columns := []string{"id", "city_eng"}

			values := make([][]any, n)
			for i := range values {
				values[i] = []any{int64(i + 1), "Istanbul"}

			}
			data := newFakeRows(columns, values...)
			q := &scriptedQuerier{rows: []pgx.Rows{data}}
			query := page.Must("SELECT id,city_eng FROM warehouses", page.Tie(page.Asc("id")))
			statements, err := query.Build(reflect.TypeFor[warehouse](), page.Request{Number: 1, Size: 2}, 0)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := collectPageRows[warehouse](context.Background(), q, statements, nil)
			if err != nil || len(rows) != n || len(q.sql) != 1 || !data.closed {
				t.Fatalf("rows=%v queries=%d err=%v", rows, len(q.sql), err)
			}
		})
	}

}

// A transaction with only Query implemented catches accidental count calls.
type pageRowsTx struct {
	pgx.Tx
	querier *scriptedQuerier
}

func (t pageRowsTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.querier.Query(ctx, sql, args...)
}
func TestTxFetchPageRows(t *testing.T) {

	for _, n := range []int{0, 1, 2} {
		columns := []string{"id", "city_eng"}

		values := make([][]any, n)
		for i := range values {
			values[i] = []any{int64(i + 1), "Istanbul"}

		}
		driver := &scriptedQuerier{rows: []pgx.Rows{newFakeRows(columns, values...)}}
		tx := wrapTx(pageRowsTx{querier: driver})
		q := page.Must("SELECT id,city_eng FROM warehouses", page.Tie(page.Asc("id")))
		rows, err := tx.FetchPageRows[warehouse](context.Background(), q, page.Request{Number: 1, Size: 2})
		if err != nil || len(rows) != n || len(driver.sql) != 1 {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
		_, err = tx.FetchPageRows[warehouse](context.Background(), q, page.Request{})
		if !errors.Is(err, page.ErrInvalidRequest) || len(driver.sql) != 1 {
			t.Fatalf("invalid request reached driver: %v", err)
		}
	}

}

func TestCollectAfterLookahead(t *testing.T) {
	query := page.Must("SELECT id,city_eng FROM warehouses", page.Tie(page.Asc("id")))
	for _, n := range []int{0, 1, 2, 3} {
		data := warehouseRows()
		for i := 0; i < n; i++ {
			data.values = append(data.values, []any{int64(i + 1), "Istanbul"})
		}
		q := &scriptedQuerier{rows: []pgx.Rows{data}}
		result, err := collectAfter[warehouse](context.Background(), q, query, page.CursorRequest{Size: 2}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.List) != min(n, 2) || result.HasMore != (n > 2) || (result.NextCursor != "") != (n > 2) || !data.closed || len(q.sql) != 1 {
			t.Fatalf("n=%d result=%+v", n, result)
		}
		if n > 2 {
			st, err := query.BuildAfter(nil, page.CursorRequest{Size: 2, After: result.NextCursor})
			if err != nil {
				t.Fatal(err)
			}
			if st.Args[0] != int64(2) {
				t.Fatal("cursor used lookahead row", st.Args)
			}
		}
	}
}

func TestCollectAfterErrorsCloseRows(t *testing.T) {
	want := errors.New("scan failed")
	data := warehouseRows(1)
	data.scanErr = want
	q := &scriptedQuerier{rows: []pgx.Rows{data}}
	query := page.Must("SELECT id,city_eng FROM warehouses", page.Tie(page.Asc("id")))
	if _, err := collectAfter[warehouse](context.Background(), q, query, page.CursorRequest{Size: 2}, nil); !errors.Is(err, want) || !data.closed {
		t.Fatal(err)
	}
	data = newFakeRows([]string{"city_eng"}, []any{"Istanbul"})
	q = &scriptedQuerier{rows: []pgx.Rows{data}}
	if _, err := collectAfter[warehouse](context.Background(), q, query, page.CursorRequest{Size: 2}, nil); !errors.Is(err, page.ErrInvalidColumn) || !data.closed {
		t.Fatal(err)
	}
	data = warehouseRows(1)
	data.err = want
	q = &scriptedQuerier{rows: []pgx.Rows{data}}
	if _, err := collectAfter[warehouse](context.Background(), q, query, page.CursorRequest{Size: 2}, nil); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func BenchmarkCollectPagination(b *testing.B) {
	query := page.Must("SELECT id,city_eng FROM warehouses", page.Tie(page.Asc("id")))
	for _, n := range []int{0, 20, 21} {
		b.Run(
			fmt.Sprint(n),
			func(b *testing.B) {
				rows := warehouseRows()
				for i := 0; i < n; i++ {
					rows.values = append(rows.values, []any{int64(i + 1), "Istanbul"})
				}
				q := fakeQuerier{rows: rows}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					rows.index = 0
					rows.closed = false
					if _, err := collectAfter[warehouse](context.Background(), q, query, page.CursorRequest{Size: 20}, nil); err != nil {
						b.Fatal(err)
					}
				}
			},
		)
	}
}

// cursorScanRows fails if the collector decodes every column a second time.
type cursorScanRows struct {
	*fakeRows
	keyScans    int
	mutateBytes bool
}

func (r *cursorScanRows) Values() ([]any, error) {
	return nil, errors.New("Values must not decode the whole row")
}
func (r *cursorScanRows) Scan(dest ...any) error {
	if len(dest) == 2 && dest[1] == nil {
		r.keyScans++
	}
	return r.fakeRows.Scan(dest...)
}
func (r *cursorScanRows) Next() bool {
	if r.mutateBytes && r.current != nil {
		if value, ok := r.current[0].([]byte); ok {
			for i := range value {
				value[i] = 0
			}
		}
	}
	return r.fakeRows.Next()
}

func TestCursorReadsOnlyKeys(t *testing.T) {
	rows := &cursorScanRows{fakeRows: warehouseRows(1, 2, 3)}
	q := page.Must("SELECT id,city_eng FROM warehouses", page.Tie(page.Asc("id")))
	result, err := collectAfter[warehouse](context.Background(), fakeQuerier{rows: rows}, q, page.CursorRequest{Size: 2}, nil)
	if err != nil || !result.HasMore || rows.keyScans != 1 {
		t.Fatalf("result=%+v scans=%d err=%v", result, rows.keyScans, err)
	}
}

func TestCursorLastFullPageNeedsNoToken(t *testing.T) {
	type row struct {
		Key map[string]any `db:"key"`
	}
	q := page.Must("SELECT key FROM items", page.Tie(page.Asc("key")))
	for _, n := range []int{1, 2} {
		rows := newFakeRows([]string{"key"}, []any{map[string]any{"n": 1}})
		if n == 2 {
			rows.values = append(rows.values, []any{map[string]any{"n": 2}})
		}
		result, err := collectAfter[row](context.Background(), fakeQuerier{rows: rows}, q, page.CursorRequest{Size: 1}, nil)
		if n == 1 {
			if err != nil || result.HasMore || result.NextCursor != "" || len(result.List) != 1 {
				t.Fatalf("%+v %v", result, err)
			}
		} else if !errors.Is(err, page.ErrCursorValue) {
			t.Fatal(err)
		}
		if !rows.closed {
			t.Fatal("rows left open")
		}
	}
}

func TestCursorDetachesByteKeys(t *testing.T) {
	type row struct {
		Key   []byte `db:"key"`
		Label string `db:"label"`
	}
	rows := &cursorScanRows{fakeRows: newFakeRows([]string{"key", "label"}, []any{[]byte{1, 2}, "first"}, []any{[]byte{3, 4}, "second"}), mutateBytes: true}
	q := page.Must("SELECT key,label FROM items", page.Tie(page.Asc("key")))
	result, err := collectAfter[row](context.Background(), fakeQuerier{rows: rows}, q, page.CursorRequest{Size: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := q.BuildAfter(nil, page.CursorRequest{Size: 1, After: result.NextCursor})
	if err != nil || !reflect.DeepEqual(st.Args[0], []byte{1, 2}) {
		t.Fatalf("args=%v err=%v", st.Args, err)
	}
}
