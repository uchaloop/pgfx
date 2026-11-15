package pgfxtest_test

import (
	"context"
	"fmt"
	"time"

	pgfxtest "github.com/uchaloop/pgfx/test"
)

func ExampleMakeDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgfxtest.MakeDB(ctx)
	if err != nil {
		return
	}
	defer db.Close()

	value, err := db.FetchValue[int](ctx, "SELECT 1")
	if err != nil {
		return
	}

	fmt.Println(value)
}
