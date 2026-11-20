package pgfx

import (
	"context"
	"errors"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uchaloop/pgfx/page"
)

// pageCapacity caps the list capacity reserved from Bounds.Limit.
const pageCapacity = 1000

// collectPage checks the page contract, reads one page and, when the page lies
// past the end of a non-empty result, reads the total from the first row.
func collectPage[T any](ctx context.Context, q querier, sql string, args pgx.NamedArgs, spec page.Spec) (page.Result[T], error) {
	var (
		b     page.Bounds
		order string
	)

	switch v := spec.(type) {
	case page.Bounds:
		b = v
	case *page.Bounds:
		if v != nil {
			b = *v
		}
	case page.Selection:
		b, order = v.PageParts()
	case *page.Selection:
		if v != nil {
			b, order = v.PageParts()
		}
	}

	if b.Limit < 1 || b.Offset < 0 {
		return page.Result[T]{}, page.ErrBounds
	}

	if len(order) == 0 && !hasPageBounds(sql) {
		return page.Result[T]{}, errors.New("page query must use @limit and @offset")
	}

	_, isLimitSet := args["limit"]
	_, isOffsetSet := args["offset"]

	if isLimitSet || isOffsetSet {
		return page.Result[T]{}, errors.New("page args must not set limit or offset")
	}

	bind := func(b page.Bounds) []any {
		named := maps.Clone(args)
		if named == nil {
			named = make(pgx.NamedArgs, 2)
		}

		named["limit"], named["offset"] = b.Limit, b.Offset
		return []any{named}
	}

	if len(order) > 0 {
		if _, isSet := args["order"]; isSet {
			return page.Result[T]{}, errors.New("page args must not set order")
		}

		var err error
		sql, bind, err = preparePage(sql, order, args)
		if err != nil {
			return page.Result[T]{}, err
		}
	}

	result, err := fetchPage[T](
		ctx,
		q,
		sql,
		bind,
		b,
	)
	if err != nil {
		return page.Result[T]{}, err
	}

	if len(result.List) > 0 || b.Offset == 0 {
		return result, nil
	}

	first, err := fetchPage[T](
		ctx,
		q,
		sql,
		bind,
		page.Bounds{Limit: 1},
	)
	if err != nil {
		return page.Result[T]{}, err
	}

	result.Total = first.Total

	return result, nil
}

// fetchPage runs the page query once with b bound to @limit and @offset.
func fetchPage[T any](ctx context.Context, q querier, sql string, bind func(page.Bounds) []any, b page.Bounds) (page.Result[T], error) {
	rows, err := q.Query(ctx, sql, bind(b)...)
	if err != nil {
		return page.Result[T]{}, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	if len(fields) == 0 || fields[len(fields)-1].Name != "total" {
		rows.Close()

		if err := rows.Err(); err != nil {
			return page.Result[T]{}, err
		}

		return page.Result[T]{}, errors.New("page query must end with a total column")
	}

	result := page.Result[T]{List: make([]T, 0, min(b.Limit, pageCapacity))}
	row := totalRow{Rows: rows, total: &result.Total}

	for rows.Next() {
		if int64(len(result.List)) >= b.Limit {
			return page.Result[T]{}, errors.New("page query returned more rows than requested")
		}

		item, err := pgx.RowToStructByNameLax[T](row)
		if err != nil {
			return page.Result[T]{}, err
		}

		result.List = append(result.List, item)
	}

	if err := rows.Err(); err != nil {
		return page.Result[T]{}, err
	}

	return result, nil
}

// totalRow hides the trailing total column from struct scanning and scans it
// into total instead.
type totalRow struct {
	pgx.Rows
	total *int64
}

func (r totalRow) FieldDescriptions() []pgconn.FieldDescription {
	fields := r.Rows.FieldDescriptions()

	return fields[:len(fields)-1]
}

func (r totalRow) Scan(dest ...any) error {
	return r.Rows.Scan(append(dest, r.total)...)
}

func (r totalRow) Values() ([]any, error) {
	values, err := r.Rows.Values()
	if err != nil || len(values) == 0 {
		return values, err
	}

	return values[:len(values)-1], nil
}

func (r totalRow) RawValues() [][]byte {
	values := r.Rows.RawValues()
	if len(values) == 0 {
		return values
	}

	return values[:len(values)-1]
}
