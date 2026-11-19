package pgfx_test

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"go.uber.org/fx"
)

// Database examples are compiled but not run: they require a local orders database.
func makeExampleDB() *pgfx.DB {
	db, err := pgfx.MakeDB(context.Background(), pgfx.Config{Host: "localhost:5432", Database: "orders"})
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

func ExampleMakeDB() {
	db, err := pgfx.MakeDB(
		context.Background(),
		pgfx.Config{Host: "localhost:5432", Database: "orders"},
		pgfx.WithRuntimeParam("application_name", "orders-api"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	// MakeDB creates the pool; Ping explicitly verifies connectivity.
	if err := db.Ping(context.Background()); err != nil {
		log.Print(err)
	}
}

func ExampleMakeModule() {
	fx.New(
		fx.Supply(pgfx.Config{Host: "localhost:5432", Database: "orders"}),
		pgfx.MakeModule(),
		fx.Invoke(registerExampleQuery),
	).Run()
}

func ExampleMakeModuleFor() {
	fx.New(
		fx.Supply(fx.Annotate(
			pgfx.Config{Host: "replica:5432", Database: "orders"},
			fx.ResultTags(`name:"replica"`),
		)),
		pgfx.MakeModuleFor("replica"),
		fx.Invoke(fx.Annotate(registerExampleQuery, fx.ParamTags(``, `name:"replica"`))),
	).Run()
}

// registerExampleQuery consumes the handle so Fx constructs the pool provider.
func registerExampleQuery(lc fx.Lifecycle, db *pgfx.DB) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			value, err := db.FetchValue[int](ctx, "SELECT 1")
			if err != nil {
				return err
			}

			fmt.Println(value)

			return nil
		},
	})
}

func ExampleDB_FetchRow() {
	db := makeExampleDB()
	defer db.Close()

	const query = `
		SELECT
			id,
			amount,
			meta
		FROM orders
		WHERE id = $1
	`

	item, err := db.FetchRow[order](context.Background(), query, 42)
	if err != nil {
		log.Print(err)

		return
	}

	fmt.Println(item.ID, item.Amount, item.Meta)
}

func ExampleDB_FetchRows() {
	db := makeExampleDB()
	defer db.Close()

	const query = `
		SELECT
			id,
			amount,
			meta
		FROM orders
		WHERE amount >= $1
		ORDER BY id
	`

	list, err := db.FetchRows[order](context.Background(), query, 100)
	if err != nil {
		log.Print(err)

		return
	}

	fmt.Println(list)
}

func ExampleDB_FetchValue() {
	db := makeExampleDB()
	defer db.Close()

	const query = `
		INSERT INTO orders (
			amount
		)
		VALUES (
			$1
		)
		RETURNING
			id
	`

	id, err := db.FetchValue[int64](context.Background(), query, 100)
	if err != nil {
		log.Print(err)

		return
	}

	fmt.Println(id)
}

func ExampleDB_FetchValues() {
	db := makeExampleDB()
	defer db.Close()

	const query = `
		SELECT
			id
		FROM orders
		WHERE amount >= $1
		ORDER BY id
	`

	ids, err := db.FetchValues[int64](context.Background(), query, 100)
	if err != nil {
		log.Print(err)

		return
	}

	fmt.Println(ids)
}

func ExampleDB_Transaction() {
	db := makeExampleDB()
	defer db.Close()

	ctx := context.Background()
	err := db.Transaction(
		ctx,
		pgx.TxOptions{},
		func(tx *pgfx.Tx) error {
			const query = `
				SELECT
					id,
					amount,
					meta
				FROM orders
				WHERE id = $1
				FOR UPDATE
			`

			item, err := tx.FetchRow[order](ctx, query, 42)
			if err != nil {
				return err
			}

			const updateQuery = `
				UPDATE orders
				SET
					amount = $1
				WHERE id = $2
				RETURNING
					id
			`

			_, err = tx.FetchValue[int64](
				ctx,
				updateQuery,
				item.Amount+100,
				item.ID,
			)

			return err // nil commits; an error rolls back.
		},
	)
	if err != nil {
		log.Print(err)
	}
}

func ExampleTx_Transaction() {
	db := makeExampleDB()
	defer db.Close()

	ctx := context.Background()
	err := db.Transaction(
		ctx,
		pgx.TxOptions{},
		func(tx *pgfx.Tx) error {
			return tx.Transaction(
				ctx,
				func(nested *pgfx.Tx) error {
					const query = `
						INSERT INTO orders (
							amount
						)
						VALUES (
							$1
						)
						RETURNING
							id
					`

					_, err := nested.FetchValue[int64](ctx, query, 100)

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
	db, err := pgfx.MakeDB(
		context.Background(),
		pgfx.Config{Host: "localhost:5432", Database: "orders"},
		pgfx.WithQueryMetrics(
			func(metric pgfx.QueryMetric) {
				// log.Printf is concurrency-safe. Production callbacks should be cheap.
				log.Printf(
					"name=%s kind=%s duration=%s rows=%d err=%v",
					metric.Name,
					metric.Kind,
					metric.Duration,
					metric.RowsAffected,
					metric.Err,
				)
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

func ExampleTx_BeginNested() {
	db := makeExampleDB()
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

			const query = `
				INSERT INTO orders (
					amount
				)
				VALUES (
					$1
				)
				RETURNING
					id
			`

			if _, err := nested.FetchValue[int64](ctx, query, 100); err != nil {
				return err
			}

			return nested.Commit(ctx) // Releases this savepoint, not the outer transaction.
		},
	)
	if err != nil {
		log.Print(err)
	}
}

func ExampleMakeModule_types() {
	registerTypes := func(ctx context.Context, conn *pgx.Conn) error {
		for _, name := range []string{"public.order_status", "public.order_status[]"} {
			typ, err := conn.LoadType(ctx, name)
			if err != nil {
				return err
			}

			conn.TypeMap().RegisterType(typ)
		}

		return nil
	}

	// The migration creating order_status must run before application startup.
	_ = fx.New(
		fx.Supply(pgfx.Config{Host: "localhost:5432", Database: "orders"}),
		pgfx.MakeModule(pgfx.WithAfterConnect(registerTypes)),
		fx.Invoke(func(lc fx.Lifecycle, db *pgfx.DB) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					return db.Ping(ctx)
				},
			})
		}),
	)
}

func ExampleMakeModule_poolHooks() {
	_ = fx.New(
		fx.Supply(pgfx.Config{Host: "localhost:5432", Database: "orders"}),
		pgfx.MakeModule(pgfx.WithPrepareConn(func(ctx context.Context, conn *pgx.Conn) (bool, error) {
			const query = `
				SELECT
					set_config('statement_timeout', '5s', false)
			`

			_, err := conn.Exec(ctx, query)

			if err != nil {
				return false, err
			}

			return true, nil
		})),
		fx.Invoke(registerExampleQuery),
	)
}
