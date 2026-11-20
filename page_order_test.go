package pgfx

import (
	"strings"
	"testing"
)

func TestPageOrderMarker(t *testing.T) {
	const prefix = `select '@order', "@order", $tag$@order$tag$, E'x\'@order', @orderID /* nested /* @order */ */ -- @order
order by `

	got, _, err := preparePage(prefix+"@order limit @limit offset @offset", "id DESC", map[string]any{"orderID": 1})
	if got != strings.Replace(prefix, "@orderID", "$3", 1)+"id DESC limit $1 offset $2" || err != nil {
		t.Fatal(got, err)
	}

	for _, sql := range []string{"select '@order' limit @limit offset @offset", "select @order, @order limit @limit offset @offset"} {
		if _, _, err := preparePage(sql, "id", nil); err == nil {
			t.Fatal(sql)
		}
	}
}

func TestSkipPageDollarQuote(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want int
	}{
		{sql: "$$@order$$ rest", want: 10},
		{sql: "$a$@order$a$ rest", want: 12},
		{sql: "$a$unfinished", want: 13},
		{sql: "$1", want: 1},
	} {
		if got := skipPageDollarQuote(tc.sql, 0); got != tc.want {
			t.Errorf("%q: next = %d, want %d", tc.sql, got, tc.want)
		}
	}
}
