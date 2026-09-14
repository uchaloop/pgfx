package pgfx_test

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/page"
	"go.uber.org/fx"
)

// Database examples are compiled but not run: they require a local orders database.
func exampleDB() *pgfx.DB {
	db, err := pgfx.Make(context.Background(), pgfx.Config{Host: "localhost:5432", Database: "orders"})
	if err != nil {
		log.Fatal(err)
	}
	return db
}

type order struct {
	ID     int64          `db:"id" json:"id"`
	Amount int64          `db:"amount" json:"amount"`
	Meta   map[string]any `db:"meta" json:"meta"`
}

func ExampleMake() {
	db, err := pgfx.Make(
		context.Background(),
		pgfx.Config{Host: "localhost:5432", Database: "orders"},
		pgfx.WithRuntimeParam("application_name", "orders-api"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	// Make creates the pool; Ping explicitly verifies connectivity.
	if err := db.Ping(context.Background()); err != nil {
		log.Print(err)
	}
}

func ExampleModule() {
	fx.New(fx.Supply(pgfx.Config{Host: "localhost:5432", Database: "orders"}), pgfx.Module).Run()
}

func ExampleModuleFor() {
	fx.New(
		fx.Supply(fx.Annotate(pgfx.Config{Host: "replica:5432", Database: "orders"}, fx.ResultTags(`name:"replica"`))),
		pgfx.ModuleFor("replica"),
	).Run()
}

func ExampleDB_FetchRow() {
	db := exampleDB()
	defer db.Close()
	item, err := db.FetchRow[order](context.Background(), `SELECT id, amount, meta FROM orders WHERE id=$1`, 42)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(item.ID, item.Amount, item.Meta)
}

func ExampleDB_FetchRows() {
	db := exampleDB()
	defer db.Close()
	list, err := db.FetchRows[order](context.Background(), `SELECT id, amount, meta FROM orders WHERE amount >= $1 ORDER BY id`, 100)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(list)
}

func ExampleDB_FetchValue() {
	db := exampleDB()
	defer db.Close()
	id, err := db.FetchValue[int64](context.Background(), `INSERT INTO orders (amount) VALUES ($1) RETURNING id`, 100)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(id)
}

func ExampleDB_FetchValues() {
	db := exampleDB()
	defer db.Close()
	ids, err := db.FetchValues[int64](context.Background(), `SELECT id FROM orders WHERE amount >= $1 ORDER BY id`, 100)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(ids)
}

func ExampleDB_FetchPage_offsetLimit() {
	db := exampleDB()
	defer db.Close()
	query := page.Must(
		`SELECT id, amount, meta FROM orders WHERE amount >= $1`,
		page.Tie(page.Asc("id")),
		page.SortKeyTag("json"),
	)
	list, total, err := db.FetchPage[order](
		context.Background(),
		query,
		page.Request{Number: 2, Size: 20, Sort: []string{"amount:desc"}},
		100,
	)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(list, total)
}

func ExampleDB_FetchPageRows() {
	db := exampleDB()
	defer db.Close()
	query := page.Must(`SELECT id, amount, meta FROM orders WHERE amount >= $1`, page.Tie(page.Asc("id")))
	// No count statement runs; more says whether another page follows.
	list, more, err := db.FetchPageRows[order](context.Background(), query, page.Request{Number: 1, Size: 20}, 100)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(list, more)
}

func ExampleDB_Transaction() {
	db := exampleDB()
	defer db.Close()
	ctx := context.Background()
	err := db.Transaction(
		ctx,
		pgx.TxOptions{},
		func(tx *pgfx.Tx) error {
			item, err := tx.FetchRow[order](ctx, `SELECT id, amount, meta FROM orders WHERE id=$1 FOR UPDATE`, 42)
			if err != nil {
				return err
			}
			_, err = tx.FetchValue[int64](ctx, `UPDATE orders SET amount=$1 WHERE id=$2 RETURNING id`, item.Amount+100, item.ID)
			return err // nil commits; an error rolls back.
		},
	)
	if err != nil {
		log.Print(err)
	}
}

func ExampleTx_FetchPage() {
	db := exampleDB()
	defer db.Close()
	ctx := context.Background()
	query := page.Must(`SELECT id, amount, meta FROM orders`, page.Tie(page.Asc("id")))
	var list []order
	var total uint
	err := db.Transaction(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx *pgfx.Tx) error {
			var err error
			list, total, err = tx.FetchPage[order](ctx, query, page.Request{Number: 1, Size: 20})
			return err
		},
	)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(list, total) // Read from one explicitly requested snapshot.
}

func ExampleTx_Transaction() {
	db := exampleDB()
	defer db.Close()
	ctx := context.Background()
	err := db.Transaction(
		ctx,
		pgx.TxOptions{},
		func(tx *pgfx.Tx) error {
			return tx.Transaction(
				ctx,
				func(nested *pgfx.Tx) error {
					_, err := nested.FetchValue[int64](ctx, `INSERT INTO orders (amount) VALUES ($1) RETURNING id`, 100)
					return err
				},
			) // Releases the savepoint on nil, rolls back to it on error.
		},
	)
	if err != nil {
		log.Print(err)
	}
}

func ExampleWithQueryName() {
	ctx := pgfx.WithQueryName(context.Background(), "orders.list")
	fmt.Println(pgfx.QueryName(ctx))
	// Output: orders.list
}

func ExampleWithQueryMetrics() {
	db, err := pgfx.Make(
		context.Background(),
		pgfx.Config{Host: "localhost:5432", Database: "orders"},
		pgfx.WithQueryMetrics(
			func(metric pgfx.QueryMetric) {
				// log.Printf is concurrency-safe. Production callbacks should be cheap.
				log.Printf("name=%s kind=%s duration=%s rows=%d err=%v", metric.Name, metric.Kind, metric.Duration, metric.RowsAffected, metric.Err)
			},
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	_, err = db.FetchValues[int64](pgfx.WithQueryName(context.Background(), "orders.ids"), `SELECT id FROM orders ORDER BY id`)
	if err != nil {
		log.Print(err)
	}
}

func ExampleTx_FetchPageRows() {
	db := exampleDB()
	defer db.Close()
	ctx := context.Background()
	query := page.Must(`SELECT id, amount, meta FROM orders`, page.Tie(page.Asc("id")))
	err := db.Transaction(
		ctx,
		pgx.TxOptions{AccessMode: pgx.ReadOnly},
		func(tx *pgfx.Tx) error {
			list, more, err := tx.FetchPageRows[order](ctx, query, page.Request{Number: 1, Size: 20})
			if err != nil {
				return err
			}
			fmt.Println(list, more)
			return nil
		},
	)
	if err != nil {
		log.Print(err)
	}
}

func ExampleTx_BeginNested() {
	db := exampleDB()
	defer db.Close()
	ctx := context.Background()
	err := db.Transaction(
		ctx,
		pgx.TxOptions{},
		func(tx *pgfx.Tx) error {
			nested, err := tx.BeginNested(ctx)
			if err != nil {
				return err
			}
			defer nested.Rollback(ctx)
			if _, err := nested.FetchValue[int64](ctx, `INSERT INTO orders (amount) VALUES ($1) RETURNING id`, 100); err != nil {
				return err
			}
			return nested.Commit(ctx) // Releases this savepoint, not the outer transaction.
		},
	)
	if err != nil {
		log.Print(err)
	}
}

func ExampleDB_FetchAfter() {
	db := exampleDB()
	defer db.Close()
	query := page.Must("SELECT id, amount, meta FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))
	req := page.CursorRequest{Size: 20}
	for {
		result, err := db.FetchAfter[order](context.Background(), query, req, 100)
		if err != nil {
			log.Print(err)
			return
		}
		for _, item := range result.List {
			fmt.Println(item.ID)
		}
		if !result.HasMore {
			break
		}
		req.After = result.NextCursor
	}
}

func ExampleDB_FetchTotal() {
	db := exampleDB()
	defer db.Close()
	query := page.Must("SELECT id, amount, meta FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))
	total, err := db.FetchTotal(context.Background(), query, 100)
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(total)
}

func ExampleTx_FetchAfter() {
	db := exampleDB()
	defer db.Close()
	ctx := context.Background()
	query := page.Must("SELECT id, amount, meta FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))
	err := db.Transaction(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx *pgfx.Tx) error {
			result, err := tx.FetchAfter[order](ctx, query, page.CursorRequest{Size: 20}, 100)
			if err != nil {
				return err
			}
			total, err := tx.FetchTotal(ctx, query, 100)
			if err != nil {
				return err
			}
			fmt.Println(len(result.List), result.HasMore, total)
			return nil
		},
	)
	if err != nil {
		log.Print(err)
	}
}
