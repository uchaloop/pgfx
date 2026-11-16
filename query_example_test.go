package pgfx_test

import (
	"context"
	"fmt"
	"time"

	pgfxtest "github.com/uchaloop/pgfx/test"
)

// Database examples use PGFX_TEST_* and are exercised by TestDocumentationExamples.
func ExampleDB_FetchOptionalRow() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	type user struct {
		ID int64 `db:"id"`
	}

	for _, id := range []int64{1, 2} {
		value, isFound, err := db.FetchOptionalRow[user](ctx, "SELECT 1::bigint AS id WHERE $1::bigint = 1", id)
		if err != nil {
			panic(err)
		}

		if !isFound {
			fmt.Println("not found")
			continue
		}

		fmt.Println(value.ID)
	}
	// Prints 1, then not found.
}

func ExampleDB_FetchRowsInto() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	type row struct {
		ID int64 `db:"id"`
	}

	rows := make([]row, 0, 8)

	for _, id := range []int64{1, 2} {
		rows, err = db.FetchRowsInto(
			ctx,
			rows[:0],
			"SELECT $1::bigint AS id",
			id,
		)
		if err != nil {
			panic(err)
		}

		// Finish consuming the result before the next iteration reuses its storage.
		fmt.Println(rows[0].ID)
	}
	// Prints 1, then 2.
}

func ExampleDB_FetchValuesInto() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	values := make([]int64, 0, 8)

	for _, id := range []int64{1, 2} {
		values, err = db.FetchValuesInto(
			ctx,
			values[:0],
			"SELECT $1::bigint",
			id,
		)
		if err != nil {
			panic(err)
		}

		fmt.Println(values)
	}
	// Prints [1], then [2]. Each result is consumed before storage is reused.
}
