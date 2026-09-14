package page_test

import (
	"fmt"
	"reflect"

	"github.com/uchaloop/pgfx/page"
)

func ExampleQuery_Build() {
	query := page.Must("SELECT id FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))
	statements, err := query.Build(nil, page.Request{Number: 2, Size: 20}, 1)
	if err != nil {
		panic(err)
	}

	// The rows statement reads one row past the page to tell whether another
	// page follows.
	fmt.Println(statements.Rows)
	fmt.Println(statements.PagingArgs)
	fmt.Println(statements.Count)
	// Output:
	// SELECT * FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page ORDER BY "id" ASC LIMIT $2 OFFSET $3
	// [21 20]
	// SELECT count(*) FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page
}

func ExampleSortable() {
	query := page.Must(
		"SELECT id, title, updated_at FROM articles",
		page.Tie(page.Asc("id")),
		page.Sortable(page.Cols{"title": "title", "updatedAt": "updated_at"}),
	)

	statements, err := query.BuildRows(nil, page.Request{Number: 1, Size: 20, Sort: []string{"updatedAt:desc"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(statements.Rows)

	// A field the whitelist does not list is an error, not a silent skip.
	_, err = query.BuildRows(nil, page.Request{Number: 1, Size: 20, Sort: []string{"body"}}, 0)
	fmt.Println(err)
	// Output:
	// SELECT * FROM (SELECT id, title, updated_at FROM articles) AS pgfx_page ORDER BY "updated_at" DESC, "id" DESC LIMIT $1 OFFSET $2
	// unknown sort field: "body"
}

func ExampleTie() {
	query := page.Must(
		"SELECT id, updated_at FROM articles",
		page.Tie(page.Desc("id")),
		page.Sortable(page.Cols{"updatedAt": "updated_at"}),
	)

	// Alone, the tie keeps its own direction: the newest rows come first.
	alone, err := query.BuildRows(nil, page.Request{Number: 1, Size: 20}, 0)
	if err != nil {
		panic(err)
	}

	// After another order it takes that order's direction, so an index on
	// (updated_at, id) serves the whole order in one scan.
	after, err := query.BuildRows(nil, page.Request{Number: 1, Size: 20, Sort: []string{"updatedAt"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(alone.Rows)
	fmt.Println(after.Rows)
	// Output:
	// SELECT * FROM (SELECT id, updated_at FROM articles) AS pgfx_page ORDER BY "id" DESC LIMIT $1 OFFSET $2
	// SELECT * FROM (SELECT id, updated_at FROM articles) AS pgfx_page ORDER BY "updated_at" ASC, "id" ASC LIMIT $1 OFFSET $2
}

func ExampleSortKeyTag() {
	type warehouse struct {
		ID   int64  `db:"id" json:"id"`
		City string `db:"city_eng" json:"cityEng"`
	}

	// Every field of the model becomes sortable under its json name.
	query := page.Must("SELECT id, city_eng FROM warehouses", page.Tie(page.Asc("id")), page.SortKeyTag("json"))

	statements, err := query.BuildRows(reflect.TypeFor[warehouse](), page.Request{Number: 1, Size: 10, Sort: []string{"cityEng:desc"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(statements.Rows)
	// Output:
	// SELECT * FROM (SELECT id, city_eng FROM warehouses) AS pgfx_page ORDER BY "city_eng" DESC, "id" DESC LIMIT $1 OFFSET $2
}

func ExampleCountSQL() {
	query := page.Must(
		"SELECT w.id, c.name FROM warehouses w JOIN countries c ON c.id=w.country_id WHERE w.country_id=$1",
		page.Tie(page.Asc("id")),
		// Equivalent if the mandatory country reference points to one unique row.
		page.CountSQL("SELECT 1 FROM warehouses WHERE country_id=$1"),
	)

	count, err := query.BuildCount()
	if err != nil {
		panic(err)
	}

	fmt.Println(count)
	// Output:
	// SELECT count(*) FROM (SELECT 1 FROM warehouses WHERE country_id=$1) AS pgfx_page
}

func ExampleHead() {
	query := page.Must(
		"SELECT id, is_active, name FROM warehouses",
		page.Head(page.Desc("is_active")),
		page.Tie(page.Asc("id")),
		page.Sortable(page.Cols{"name": "name"}),
	)

	statements, err := query.BuildRows(nil, page.Request{Number: 1, Size: 20, Sort: []string{"name"}}, 0)
	if err != nil {
		panic(err)
	}

	fmt.Println(statements.Rows)
	// Output:
	// SELECT * FROM (SELECT id, is_active, name FROM warehouses) AS pgfx_page ORDER BY "is_active" DESC, "name" ASC, "id" ASC LIMIT $1 OFFSET $2
}

func ExampleQuery_BuildAfter() {
	query := page.Must("SELECT id FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))

	statement, err := query.BuildAfter(nil, page.CursorRequest{Size: 20}, 100)
	if err != nil {
		panic(err)
	}

	fmt.Println(statement.Rows)
	fmt.Println(statement.Args)
	// Output:
	// SELECT * FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page ORDER BY "id" ASC LIMIT $2
	// [100 21]
}
