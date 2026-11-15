package page_test

import (
	"fmt"
	"reflect"

	"github.com/uchaloop/pgfx/page"
)

func ExampleQuery_BuildStatement() {
	query := page.MakeQueryOrPanic("SELECT id FROM orders WHERE amount >= $1", page.WithTie(page.MakeAscOrder("id")))
	statement, err := query.BuildStatement(nil, page.Request{Page: 2, Limit: 20}, 1)
	if err != nil {
		panic(err)
	}

	fmt.Println(statement.Rows)
	fmt.Println(statement.PageArgs)
	fmt.Println(statement.Count)
	// Output:
	// SELECT * FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page ORDER BY "id" ASC LIMIT $2 OFFSET $3
	// [20 20]
	// SELECT COUNT(*) FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page
}

func ExampleWithSortable() {
	query := page.MakeQueryOrPanic(
		"SELECT id, title, updated_at FROM articles",
		page.WithTie(page.MakeAscOrder("id")),
		page.WithSortable(page.Cols{"title": "title", "updatedAt": "updated_at"}),
	)

	statement, err := query.BuildStatement(nil, page.Request{Page: 1, Limit: 20, Sort: []string{"updatedAt:desc"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(statement.Rows)

	// A field the whitelist does not list is an error, not a silent skip.
	_, err = query.BuildStatement(nil, page.Request{Page: 1, Limit: 20, Sort: []string{"body"}}, 0)
	fmt.Println(err)
	// Output:
	// SELECT * FROM (SELECT id, title, updated_at FROM articles) AS pgfx_page ORDER BY "updated_at" DESC, "id" DESC LIMIT $1 OFFSET $2
	// unknown sort field: "body"
}

func ExampleWithTie() {
	query := page.MakeQueryOrPanic(
		"SELECT id, updated_at FROM articles",
		page.WithTie(page.MakeDescOrder("id")),
		page.WithSortable(page.Cols{"updatedAt": "updated_at"}),
	)

	// Alone, the tie keeps its own direction: the newest rows come first.
	alone, err := query.BuildStatement(nil, page.Request{Page: 1, Limit: 20}, 0)
	if err != nil {
		panic(err)
	}

	// After another order it takes that order's direction, so an index on
	// (updated_at, id) serves the whole order in one scan.
	after, err := query.BuildStatement(nil, page.Request{Page: 1, Limit: 20, Sort: []string{"updatedAt"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(alone.Rows)
	fmt.Println(after.Rows)
	// Output:
	// SELECT * FROM (SELECT id, updated_at FROM articles) AS pgfx_page ORDER BY "id" DESC LIMIT $1 OFFSET $2
	// SELECT * FROM (SELECT id, updated_at FROM articles) AS pgfx_page ORDER BY "updated_at" ASC, "id" ASC LIMIT $1 OFFSET $2
}

func ExampleWithSortKeyTag() {
	type warehouse struct {
		ID   int64  `db:"id" json:"id"`
		City string `db:"city_eng" json:"cityEng"`
	}

	// Both fields have explicit db tags and are exposed under their json names.
	query := page.MakeQueryOrPanic("SELECT id, city_eng FROM warehouses", page.WithTie(page.MakeAscOrder("id")), page.WithSortKeyTag("json"))

	statement, err := query.BuildStatement(reflect.TypeFor[warehouse](), page.Request{Page: 1, Limit: 10, Sort: []string{"cityEng:desc"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(statement.Rows)
	// Output:
	// SELECT * FROM (SELECT id, city_eng FROM warehouses) AS pgfx_page ORDER BY "city_eng" DESC, "id" DESC LIMIT $1 OFFSET $2
}

func ExampleWithHead() {
	query := page.MakeQueryOrPanic(
		"SELECT id, is_active, name FROM warehouses",
		page.WithHead(page.MakeDescOrder("is_active")),
		page.WithTie(page.MakeAscOrder("id")),
		page.WithSortable(page.Cols{"name": "name"}),
	)

	statement, err := query.BuildStatement(nil, page.Request{Page: 1, Limit: 20, Sort: []string{"name"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(statement.Rows)
	// Output:
	// SELECT * FROM (SELECT id, is_active, name FROM warehouses) AS pgfx_page ORDER BY "is_active" DESC, "name" ASC, "id" ASC LIMIT $1 OFFSET $2
}
