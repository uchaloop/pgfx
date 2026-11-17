package page_test

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
	pgfxtest "github.com/uchaloop/pgfx/test"
)

// These examples use PGFX_TEST_* and need no application tables.
// TestPageExamples executes them against PostgreSQL with the integration tag.
func ExampleFetch() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	type item struct {
		ID int64 `db:"id"`
	}

	// The list and count use the same filter; only the list has LIMIT/OFFSET.
	result, err := page.Fetch(
		ctx,
		func(ctx context.Context) ([]item, error) {
			return db.FetchRows[item](
				ctx,
				"SELECT id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1 ORDER BY id LIMIT $2 OFFSET $3",
				5,
				2,
				1,
			)
		},
		func(ctx context.Context) (int64, error) {
			return db.FetchValue[int64](
				ctx,
				"SELECT COUNT(*) FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1",
				5,
			)
		},
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(result.List, result.Total)
	// Prints [{7} {8}] 5.
}

func ExampleFetch_parallel() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	type item struct {
		ID int64 `db:"id"`
	}

	// Both callbacks use the pool; never share one transaction or connection.
	result, err := page.Fetch(
		ctx,
		func(ctx context.Context) ([]item, error) {
			return db.FetchRows[item](
				ctx,
				"SELECT id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1 ORDER BY id LIMIT $2 OFFSET $3",
				5,
				2,
				1,
			)
		},
		func(ctx context.Context) (int64, error) {
			return db.FetchValue[int64](ctx, "SELECT COUNT(*) FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1", 5)
		},
		page.WithParallel(),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(result.List, result.Total)
	// Prints [{7} {8}] 5.
}

func ExampleFetch_transaction() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	type item struct {
		ID int64 `db:"id"`
	}

	var result page.Result[item]

	// Both callbacks use the same snapshot and run sequentially on tx.
	err = db.Transaction(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx *pgfx.Tx) error {
			var err error
			result, err = page.Fetch(
				ctx,
				func(ctx context.Context) ([]item, error) {
					return tx.FetchRows[item](
						ctx,
						"SELECT id FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1 ORDER BY id LIMIT $2 OFFSET $3",
						5,
						2,
						1,
					)
				},
				func(ctx context.Context) (int64, error) {
					return tx.FetchValue[int64](ctx, "SELECT COUNT(*) FROM generate_series(1::bigint, 10) AS items(id) WHERE id > $1", 5)
				},
			)

			return err
		},
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(result.List, result.Total)
	// Prints [{7} {8}] 5.
}

// ExamplePager_Fetch выполняется через TestPageExamples с тегом integration.
func ExamplePager_Fetch() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	pager, err := page.MakePager(page.Config{MinLimit: 1, DefaultLimit: 2, MaxLimit: 100})
	if err != nil {
		panic(err)
	}

	type item struct {
		ID int64 `db:"id"`
	}
	result, err := pager.Fetch(
		ctx,
		db,
		page.Params{Page: 2, Limit: 2},
		func(ctx context.Context, tx *pgfx.Tx, bounds page.Bounds) ([]item, error) {
			return tx.FetchRows[item](
				ctx,
				"SELECT id FROM generate_series(1::bigint, 5) AS items(id) ORDER BY id LIMIT $1 OFFSET $2",
				bounds.Limit,
				bounds.Offset,
			)
		},
		func(ctx context.Context, tx *pgfx.Tx) (int64, error) {
			return tx.FetchValue[int64](ctx, "SELECT COUNT(*) FROM generate_series(1::bigint, 5)")
		},
	)
	if err != nil {
		panic(err)
	}

	// Проверяем результат при запуске через интеграционный тест.
	if len(result.List) != 2 || result.List[0].ID != 3 || result.List[1].ID != 4 || result.Total != 5 {
		panic("unexpected page result")
	}
	fmt.Println(result.List, result.Total)
	// Prints [{3} {4}] 5.
}
