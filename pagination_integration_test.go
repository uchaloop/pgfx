//go:build integration

package pgfx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/uchaloop/pgfx/page"
	"github.com/uchaloop/secret/v2"
)

type pageItem struct {
	ID    int64             `db:"id"    json:"id"`
	Grade string            `db:"grade" json:"grade"`
	Meta  map[string]string `db:"meta"`
}

// TestFetchPageIntegration walks a table whose sort column repeats, which is
// where a page without a tie order starts losing and duplicating rows.
func TestFetchPageIntegration(t *testing.T) {
	host := os.Getenv("PGFX_TEST_HOST")
	if host == "" {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := Make(ctx, integrationConfig(host, 5*time.Second))
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	t.Cleanup(db.Close)

	const table = "pgfx_integration_pages"

	if _, err := db.Exec(ctx, "drop table if exists "+table); err != nil {
		t.Fatalf("drop stale table: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.Exec(cleanupCtx, "drop table if exists "+table)
	})

	if _, err := db.Exec(
		ctx,
		"create table "+table+" (id bigint primary key, grade text not null, country text not null)",
	); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Ten matching rows sharing three grades, plus one row the filter excludes.
	if _, err := db.Exec(
		ctx,
		"insert into "+table+" (id, grade, country) "+
			"select i, 'g' || (i % 3), 'TR' from generate_series(1, 10) AS i",
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if _, err := db.Exec(
		ctx,
		"insert into "+table+" (id, grade, country) values (99, 'g0', 'DE')",
	); err != nil {
		t.Fatalf("seed excluded row: %v", err)
	}

	query, err := page.Make(
		"SELECT id, grade, jsonb_build_object('country', country) AS meta FROM "+table+" WHERE country = $1;",
		page.Tie(page.Asc("id")),
		page.SortKeyTag("json"),
	)
	if err != nil {
		t.Fatalf("page.Make: %v", err)
	}

	seen := make(map[int64]int, 10)
	sort := []string{"grade:desc"}

	for number := uint(1); number <= 4; number++ {
		req := page.Request{Number: number, Size: 3, Sort: sort}

		items, total, err := db.FetchPage[pageItem](ctx, query, req, "TR")
		if err != nil {
			t.Fatalf("page %d: %v", number, err)
		}

		if total != 10 {
			t.Fatalf("page %d: total = %d, want 10", number, total)
		}

		want := 3
		if number == 4 {
			want = 1
		}
		if len(items) != want {
			t.Fatalf("page %d: rows = %d, want %d", number, len(items), want)
		}

		for _, item := range items {
			seen[item.ID]++
			if item.Meta["country"] != "TR" {
				t.Fatalf("map decoding: %+v", item)
			}
		}
	}

	// The fourth page is short, so its total came from the rows rather than from
	// count(*) - the two have to agree, and every row has to appear exactly once
	// across the four pages.
	if len(seen) != 10 {
		t.Fatalf("distinct rows = %d, want 10", len(seen))
	}
	for id, times := range seen {
		if times != 1 {
			t.Fatalf("row %d appeared %d times", id, times)
		}
	}

	// A page past the end is empty, not an error, and still reports the total.
	items, total, err := db.FetchPage[pageItem](
		ctx, query, page.Request{Number: 9, Size: 3, Sort: sort}, "TR",
	)
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}
	if len(items) != 0 || total != 10 {
		t.Fatalf("rows = %d, total = %d, want 0 and 10", len(items), total)
	}

}

// cursorDB connects to the database the cursor tests run on: the pagination-lab
// stand when PGFX_CURSOR_TEST_HOST names it, otherwise the PGFX_TEST_* database
// CI provides. The tests read generated rows, so any database serves; only
// TestCursorPlans needs the populated stand.
func cursorDB(t *testing.T) (*DB, context.Context) {
	t.Helper()

	var cfg Config
	if host := os.Getenv("PGFX_CURSOR_TEST_HOST"); host != "" {
		cfg = Config{
			Host:     host,
			Database: "pagination_lab",
			User:     "pagination_lab",
			Password: secret.New("pagination_lab"),
			TLS:      TLSConfig{Mode: "disable"},
		}
	} else if host := os.Getenv("PGFX_TEST_HOST"); host != "" {
		cfg = integrationConfig(host, 5*time.Second)
	} else {
		t.Skip("neither PGFX_CURSOR_TEST_HOST nor PGFX_TEST_HOST is set")
	}

	cfg.Pool = PoolConfig{MaxConns: 1}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db, err := Make(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	return db, ctx
}

type cursorItem struct {
	ID     int64          `db:"id"`
	Rank   *int64         `db:"rank"`
	Bucket int64          `db:"bucket"`
	Meta   map[string]any `db:"meta"`
}

func TestCursorIntegration(t *testing.T) {
	db, ctx := cursorDB(t)
	base := `SELECT (i*2)::bigint AS id, CASE WHEN i%5=0 THEN NULL ELSE i%3 END::bigint AS rank, (i%2)::bigint AS bucket, CASE WHEN i%7=0 THEN NULL ELSE jsonb_build_object('n',i) END AS meta FROM generate_series(1,127) i WHERE i%4<>$1`
	for _, desc := range []bool{false, true} {
		for _, first := range []bool{false, true} {
			for _, tieDesc := range []bool{false, true} {
				direction, opposite := "ASC", "DESC"
				if desc {
					direction, opposite = "DESC", "ASC"
				}
				nulls := "LAST"
				policy := page.NullsLast
				if first {
					nulls = "FIRST"
					policy = page.NullsFirst
				}
				tieDirection := "ASC"
				if tieDesc {
					tieDirection = "DESC"
				}
				// The head mixes directions around a nullable key. The tie follows
				// the last head term, whatever direction it declares.
				expected, err := db.FetchRows[cursorItem](ctx, "SELECT * FROM ("+base+") x ORDER BY rank "+direction+" NULLS "+nulls+", bucket "+opposite+", id "+opposite, 0)
				if err != nil {
					t.Fatal(err)
				}
				q := page.Must(
					base,
					page.Head(page.Order{Column: "rank", Desc: desc, Nulls: policy}, page.Order{Column: "bucket", Desc: !desc}),
					page.Tie(page.Order{Column: "id", Desc: tieDesc}),
					page.CountSQL("SELECT * FROM missing_count_table"),
				)
				var after string
				var all []cursorItem
				for n := 0; n < 100; n++ {
					result, err := db.FetchAfter[cursorItem](ctx, q, page.CursorRequest{Size: 7, After: after}, 0)
					if err != nil {
						t.Fatalf("%s %s tie %s: %v", direction, nulls, tieDirection, err)
					}
					all = append(all, result.List...)
					if !result.HasMore {
						if result.NextCursor != "" {
							t.Fatal(result)
						}
						break
					}
					if result.NextCursor == after {
						t.Fatal("cursor did not advance")
					}
					after = result.NextCursor
				}
				if !reflect.DeepEqual(expected, all) {
					t.Fatalf("mismatch %s NULLS %s tie %s: %d vs %d", direction, nulls, tieDirection, len(expected), len(all))
				}
			}
		}
	}
	q := page.Must(base, page.Tie(page.Asc("id")))
	err := db.Transaction(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *Tx) error {
		first, err := tx.FetchAfter[cursorItem](ctx, q, page.CursorRequest{Size: 7}, 0)
		if err != nil {
			return err
		}
		next, err := tx.FetchAfter[cursorItem](ctx, q, page.CursorRequest{Size: 7, After: first.NextCursor}, 0)
		if err != nil {
			return err
		}
		total, err := tx.FetchTotal(ctx, q, 0)
		if err != nil {
			return err
		}
		if len(next.List) != 7 || total != 96 {
			t.Fatalf("%+v total=%d", next, total)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.FetchAfter[cursorItem](canceled, q, page.CursorRequest{Size: 7}, 0); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestCursorPlans(t *testing.T) {
	if os.Getenv("PGFX_CURSOR_BENCH") != "1" {
		t.Skip("PGFX_CURSOR_BENCH=1 requires the populated pagination-lab stand")
	}
	db, ctx := cursorDB(t)
	type item struct {
		ID    int64          `db:"id"`
		Rank  int64          `db:"rank"`
		Label string         `db:"label"`
		Meta  map[string]any `db:"meta"`
	}
	for _, mixed := range []bool{false, true} {
		options := []page.Option{page.Tie(page.Asc("id"))}
		keys := []any{int64(1800000)}
		if mixed {
			options = append(options, page.Head(page.Desc("rank")))
			keys = []any{int64(1000), int64(1986000)}
		}
		query := page.Must("SELECT id,score AS rank,label,meta FROM bench_rows", options...)
		initial, err := query.BuildAfter(reflect.TypeFor[item](), page.CursorRequest{Size: 20})
		if err != nil {
			t.Fatal(err)
		}
		token, err := initial.Cursor(keys)
		if err != nil {
			t.Fatal(err)
		}
		req := page.CursorRequest{Size: 20, After: token}
		st, err := query.BuildAfter(reflect.TypeFor[item](), req)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err := db.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+st.Rows, st.Args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plan []map[string]any
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		indexConds := 0
		var walk func(map[string]any)
		walk = func(n map[string]any) {
			if _, ok := n["Index Cond"]; ok {
				indexConds++
			}
			if count, ok := n["Rows Removed by Filter"].(float64); ok && count > 100 {
				t.Fatalf("linear prefix: %s", raw)
			}
			if n["Node Type"] == "Seq Scan" || n["Node Type"] == "WindowAgg" {
				t.Fatalf("unexpected full scan: %s", raw)
			}
			if children, ok := n["Plans"].([]any); ok {
				for _, c := range children {
					walk(c.(map[string]any))
				}
			}
		}
		walk(plan[0]["Plan"].(map[string]any))
		if indexConds == 0 {
			t.Fatal("no index conditions")
		}
		var times []float64
		for n := 0; n < 8; n++ {
			start := time.Now()
			result, err := db.FetchAfter[item](ctx, query, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.List) != 20 || !result.HasMore {
				t.Fatal(result)
			}
			if n > 0 {
				times = append(times, float64(time.Since(start).Nanoseconds())/1e6)
			}
		}
		slices.Sort(times)
		t.Logf("mixed=%v median=%.3fms index_conditions=%d", mixed, times[len(times)/2], indexConds)
		if path := os.Getenv("PGFX_CURSOR_PLAN_DIR"); path != "" {
			if err := os.WriteFile(fmt.Sprintf("%s/cursor-%v.json", path, mixed), raw, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCursorScalarIntegration(t *testing.T) {
	db, ctx := cursorDB(t)
	type item struct {
		ID    int64     `db:"id"`
		Stamp time.Time `db:"stamp"`
		UUID  [16]byte  `db:"uuid"`
	}
	base := `SELECT (9223372036854775700+i)::bigint AS id,
 '2026-01-01 00:00:00+00'::timestamptz+i*interval '1 microsecond' AS stamp,
 md5(i::text)::uuid AS uuid FROM generate_series(1,9) i`
	for _, column := range []string{"id", "stamp", "uuid"} {
		t.Run(column, func(t *testing.T) {
			expected, err := db.FetchRows[item](ctx, base+" ORDER BY "+column+",id")
			if err != nil {
				t.Fatal(err)
			}
			q := page.Must(base, page.Head(page.Asc(column)), page.Tie(page.Asc("id")))
			var got []item
			req := page.CursorRequest{Size: 2}
			for n := 0; n < 10; n++ {
				result, err := db.FetchAfter[item](ctx, q, req)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, result.List...)
				if !result.HasMore {
					break
				}
				req.After = result.NextCursor
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("cursor traversal differs: got %v want %v", got, expected)
			}
		})
	}
}

// Counting only DecodeValue detects a second generic decode after struct Scan.
type countingCursorCodec struct {
	pgtype.Codec
	decoded int
}

func (c *countingCursorCodec) DecodeValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	c.decoded++
	return c.Codec.DecodeValue(m, oid, format, src)
}

func TestCursorDoesNotDecodeJSONTwice(t *testing.T) {
	db, ctx := cursorDB(t)
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	typeMap := conn.Conn().TypeMap()
	original, ok := typeMap.TypeForOID(pgtype.JSONBOID)
	if !ok {
		t.Fatal("missing JSONB codec")
	}
	codec := &countingCursorCodec{Codec: original.Codec}
	typeMap.RegisterType(&pgtype.Type{Name: original.Name, OID: original.OID, Codec: codec})
	defer typeMap.RegisterType(original)
	type item struct {
		ID   int64          `db:"id"`
		Meta map[string]any `db:"meta"`
	}
	q := page.Must(`SELECT i::bigint AS id,jsonb_build_object('payload',repeat('x',65536)) AS meta FROM generate_series(1,3) i`, page.Tie(page.Asc("id")))
	result, err := collectAfter[item](ctx, conn, q, page.CursorRequest{Size: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.List) != 2 || !result.HasMore || len(result.List[0].Meta["payload"].(string)) != 65536 {
		t.Fatal("incorrect result")
	}
	if codec.decoded != 0 {
		t.Fatalf("JSONB decoded %d extra times", codec.decoded)
	}
}
