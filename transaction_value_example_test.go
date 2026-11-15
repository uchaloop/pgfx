package pgfx_test

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	pgfxtest "github.com/uchaloop/pgfx/test"
)

func ExampleDB_TransactionValue() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	value, err := db.TransactionValue(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) (int, error) {
		return tx.FetchValue[int](ctx, "SELECT 1")
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(value)
}
