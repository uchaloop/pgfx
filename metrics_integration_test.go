//go:build integration

package pgfx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestQueryMetricsIntegration drives a real batch and a real copy through the
// pool: both go through pgx paths that a plain QueryTracer never sees.
func TestQueryMetricsIntegration(t *testing.T) {
	t.Run("metrics_only", func(t *testing.T) { testQueryMetricsIntegration(t) })
	t.Run("with_tracing", func(t *testing.T) { testQueryMetricsIntegration(t, WithTracing()) })
}

func testQueryMetricsIntegration(t *testing.T, opts ...Option) {
	host := os.Getenv("PGFX_TEST_HOST")
	if host == "" {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	var (
		mu      sync.Mutex
		metrics []QueryMetric
	)

	collect := func(metric QueryMetric) {
		mu.Lock()
		defer mu.Unlock()

		metrics = append(metrics, metric)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	opts = append(opts, WithQueryMetrics(collect), WithSQLInQueryMetrics(true))
	db, err := Make(
		ctx,
		integrationConfig(host, 5*time.Second),
		opts...,
	)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	t.Cleanup(db.Close)

	// Never drop or share another test's table.
	table := fmt.Sprintf("pgfx_integration_metrics_%d", time.Now().UnixNano())
	if _, err := db.Exec(ctx, "create table "+table+" (id bigint primary key)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := db.Exec(cleanupCtx, "drop table "+table); err != nil {
			t.Errorf("cleanup table: %v", err)
		}
	})

	batch := &pgx.Batch{}
	batch.Queue("insert into " + table + " (id) values (1)")
	batch.Queue("insert into " + table + " (id) values (2)")

	batchCtx := WithQueryName(ctx, "items.sync")
	results := db.SendBatch(batchCtx, batch)
	defer results.Close()
	if err := results.Close(); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if err := results.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}

	copyCtx := WithQueryName(ctx, "items.load")
	copied, err := db.CopyFrom(
		copyCtx,
		pgx.Identifier{table},
		[]string{"id"},
		pgx.CopyFromRows([][]any{{int64(3)}, {int64(4)}, {int64(5)}}),
	)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if copied != 3 {
		t.Fatalf("copied = %d, want 3", copied)
	}

	mu.Lock()
	snapshot := append([]QueryMetric(nil), metrics...)
	mu.Unlock()

	var batchMetric, copyMetric *QueryMetric

	var batchCount, copyCount int
	for i, metric := range snapshot {
		switch metric.Kind {
		case KindBatch:
			batchCount++
			batchMetric = &snapshot[i]
		case KindCopyFrom:
			copyCount++
			copyMetric = &snapshot[i]
		case KindQuery:
		}
	}
	if batchCount != 1 || copyCount != 1 {
		t.Fatalf("observations: batch=%d, copy=%d; want one each", batchCount, copyCount)
	}

	if batchMetric == nil {
		t.Fatal("no batch metric was reported")
	}
	if batchMetric.Name != "items.sync" {
		t.Errorf("batch Name = %q, want items.sync", batchMetric.Name)
	}
	if batchMetric.RowsAffected != 2 {
		t.Errorf("batch RowsAffected = %d, want 2", batchMetric.RowsAffected)
	}
	if batchMetric.Duration <= 0 {
		t.Errorf("batch Duration = %v, want positive", batchMetric.Duration)
	}
	if batchMetric.Err != nil {
		t.Errorf("batch Err = %v, want nil", batchMetric.Err)
	}
	// Both queued statements, in order.
	if want := "insert into " + table + " (id) values (1); insert into " + table +
		" (id) values (2)"; batchMetric.SQL != want {
		t.Errorf("batch SQL = %q, want %q", batchMetric.SQL, want)
	}

	if copyMetric == nil {
		t.Fatal("no copy metric was reported")
	}
	if copyMetric.Name != "items.load" {
		t.Errorf("copy Name = %q, want items.load", copyMetric.Name)
	}
	if copyMetric.RowsAffected != 3 {
		t.Errorf("copy RowsAffected = %d, want 3", copyMetric.RowsAffected)
	}
	if copyMetric.Duration <= 0 {
		t.Errorf("copy Duration = %v, want positive", copyMetric.Duration)
	}
	if copyMetric.Err != nil {
		t.Errorf("copy Err = %v, want nil", copyMetric.Err)
	}
	if copyMetric.SQL != "" {
		t.Errorf("copy SQL = %q, want empty", copyMetric.SQL)
	}

	wantRewriteErr := errors.New("rewrite failed")
	wantCallbackErr := errors.New("callback failed")
	for _, tc := range []struct {
		name    string
		batch   *pgx.Batch
		wantErr error
	}{
		{"early_error", func() *pgx.Batch {
			b := &pgx.Batch{}
			b.Queue("select 1", failingMetricsRewriter{wantRewriteErr})
			return b
		}(), wantRewriteErr},
		{"execution_error", batchOf("insert into "+table+" values (10)", "insert into "+table+" values (10)"), nil},
		{"scan_error", func() *pgx.Batch {
			b := batchOf("select 'not-an-integer'::text")
			b.QueuedQueries[0].QueryRow(func(row pgx.Row) error {
				var n int
				return row.Scan(&n)
			})
			return b
		}(), nil},
		{"callback_error", func() *pgx.Batch {
			b := batchOf("select 1")
			b.QueuedQueries[0].Exec(func(pgconn.CommandTag) error { return wantCallbackErr })
			return b
		}(), wantCallbackErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			br := db.SendBatch(WithQueryName(ctx, tc.name), tc.batch)
			defer br.Close()
			for range 2 {
				if err := br.Close(); err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("Close = %v, want failure (%v)", err, tc.wantErr)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			var found []QueryMetric
			for _, m := range metrics {
				if m.Name == tc.name {
					found = append(found, m)
				}
			}
			if len(found) != 1 {
				t.Fatalf("metrics = %+v, want exactly one", found)
			}
			m := found[0]
			if m.Kind != KindBatch || m.Err == nil || tc.wantErr != nil && !errors.Is(m.Err, tc.wantErr) {
				t.Fatalf("unexpected error metric: %+v", m)
			}
		})
	}
	var count int
	if err := db.QueryRow(ctx, "select count(*) from "+table+" where id = 10").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed batch should roll back: count=%d, err=%v", count, err)
	}

	t.Run("copy_error", func(t *testing.T) {
		_, err := db.CopyFrom(WithQueryName(ctx, "copy_error"), pgx.Identifier{table}, []string{"id"},
			pgx.CopyFromRows([][]any{{int64(20)}, {int64(20)}}))
		if err == nil {
			t.Fatal("expected unique violation")
		}
		mu.Lock()
		defer mu.Unlock()
		var found []QueryMetric
		for _, m := range metrics {
			if m.Name == "copy_error" {
				found = append(found, m)
			}
		}
		if len(found) != 1 {
			t.Fatalf("metrics = %+v, want exactly one", found)
		}
		var pgErr *pgconn.PgError
		m := found[0]
		if m.Kind != KindCopyFrom || !errors.As(m.Err, &pgErr) || pgErr.Code != "23505" || m.SQL != "" {
			t.Fatalf("unexpected COPY error metric: %+v", m)
		}
	})
	t.Run("missing_hooks", func(t *testing.T) {
		if err := db.SendBatch(WithQueryName(ctx, "no_hooks"), &pgx.Batch{}).Close(); err != nil {
			t.Fatal(err)
		}
		_, err := db.CopyFrom(WithQueryName(ctx, "no_hooks"), pgx.Identifier{table}, []string{"missing_column"},
			pgx.CopyFromRows([][]any{{int64(1)}}))
		if err == nil {
			t.Fatal("expected statement description error")
		}
		// Pin the pgx v5.10 limitation so a dependency fix prompts a docs update.
		mu.Lock()
		defer mu.Unlock()
		for _, m := range metrics {
			if m.Name == "no_hooks" {
				t.Fatalf("unexpected metric; revisit pgx coverage docs: %+v", m)
			}
		}
	})
}

type failingMetricsRewriter struct{ err error }

func (r failingMetricsRewriter) RewriteQuery(context.Context, *pgx.Conn, string, []any) (string, []any, error) {
	return "", nil, r.err
}
