package page_test

import (
	"fmt"
	"github.com/uchaloop/pgfx/page"
	"reflect"
)

func ExampleQuery_Build() {
	query := page.Must("SELECT id FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))
	statements, err := query.Build(nil, page.Request{Number: 2, Size: 20}, 1)
	if err != nil {
		panic(err)
	}
	fmt.Println(statements.Rows)
	fmt.Println(statements.PagingArgs)
	fmt.Println(statements.Count)
	// Output:
	// SELECT * FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page ORDER BY "id" ASC LIMIT $2 OFFSET $3
	// [20 20]
	// SELECT count(*) FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page
}

func ExampleSortKeyTag() {
	type warehouse struct {
		ID   int64  `db:"id" json:"id"`
		City string `db:"city_eng" json:"cityEng"`
	}
	query := page.Must("SELECT id, city_eng FROM warehouses", page.Tie(page.Asc("id")), page.SortKeyTag("json"))
	statements, err := query.Build(reflect.TypeFor[warehouse](), page.Request{Number: 1, Size: 10, Sort: []string{"cityEng:desc"}}, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(statements.Rows)
	// Output:
	// SELECT * FROM (SELECT id, city_eng FROM warehouses) AS pgfx_page ORDER BY "city_eng" DESC, "id" ASC LIMIT $1 OFFSET $2
}

func ExampleCountSQL() {
	query := page.Must(
		"SELECT w.id, c.name FROM warehouses w JOIN countries c ON c.id=w.country_id WHERE w.country_id=$1",
		page.Tie(page.Asc("id")),
		// Equivalent if the mandatory country reference points to one unique row.
		page.CountSQL("SELECT 1 FROM warehouses WHERE country_id=$1"),
	)
	statements, err := query.Build(nil, page.Request{Number: 1, Size: 20}, 1)
	if err != nil {
		panic(err)
	}
	fmt.Println(statements.Count)
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
	statements, err := query.Build(nil, page.Request{Number: 1, Size: 20, Sort: []string{"name"}}, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(statements.Rows)
	// Output:
	// SELECT * FROM (SELECT id, is_active, name FROM warehouses) AS pgfx_page ORDER BY "is_active" DESC, "name" ASC, "id" ASC LIMIT $1 OFFSET $2
}

func ExampleQuery_BuildRows() {
	query := page.Must("SELECT id FROM orders", page.Tie(page.Asc("id")))
	st, err := query.BuildRows(nil, page.Request{Number: 1, Size: 20}, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(st.Rows)
	fmt.Println(st.Count == "")
	// Output:
	// SELECT * FROM (SELECT id FROM orders) AS pgfx_page ORDER BY "id" ASC LIMIT $1 OFFSET $2
	// true
}

func ExampleQuery_BuildAfter() {
	query := page.Must("SELECT id FROM orders WHERE amount >= $1", page.Tie(page.Asc("id")))
	st, err := query.BuildAfter(nil, page.CursorRequest{Size: 20}, 100)
	if err != nil {
		panic(err)
	}
	fmt.Println(st.Rows)
	fmt.Println(st.Args)
	// Output:
	// SELECT * FROM (SELECT id FROM orders WHERE amount >= $1) AS pgfx_page ORDER BY "id" ASC LIMIT $2
	// [100 21]
}
