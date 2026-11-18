package page_test

import (
	"fmt"

	"github.com/uchaloop/pgfx/page"
)

func ExampleSortable_OrderBy() {
	type sortField string

	const (
		sortName      sortField = "name"
		sortCreatedAt sortField = "createdAt"
	)

	sortable := page.Sortable[sortField]{
		sortName:      "o.partner_name",
		sortCreatedAt: "o.created_at",
	}

	order, err := sortable.OrderBy([]page.Key[sortField]{{Name: sortName, Desc: true}}, "o.id")
	if err != nil {
		panic(err)
	}

	fmt.Println(order)
	// Output: o.partner_name DESC, o.id DESC
}

func ExampleMakeBounds() {
	bounds, err := page.MakeBounds(3, 20)
	if err != nil {
		panic(err)
	}

	fmt.Println(bounds.Limit, bounds.Offset)
	// Output: 20 40
}
