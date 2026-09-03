package page

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// columnTagKey is the struct tag holding a model's column names. It is the tag
// pgx decodes rows by, which is what makes a derived whitelist correct by
// construction: every sortable field is a column the page actually returns.
const columnTagKey = "db"

// defaultTagKey is the struct tag holding the name a client sorts by.
const defaultTagKey = columnTagKey

// sortableCache holds the whitelist derived for a model and a tag. Deriving it
// is reflection over a type, and a type does not change.
var sortableCache sync.Map // sortableKey -> Cols

type sortableKey struct {
	model reflect.Type
	tag   string
}

// sortableOf derives the sort whitelist from a model's struct tags: the tag
// named by tagKey holds the name a client sends, the db tag (or the field name)
// holds the column it orders by.
func sortableOf(model reflect.Type, tagKey string) (Cols, error) {
	if model == nil {
		return nil, ErrUnsortableModel
	}

	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}

	if model.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: %s", ErrUnsortableModel, model)
	}

	key := sortableKey{model: model, tag: tagKey}
	if cached, ok := sortableCache.Load(key); ok {
		return cached.(Cols), nil
	}

	sortable := make(Cols)
	collectSortable(model, tagKey, sortable)

	cached, _ := sortableCache.LoadOrStore(key, sortable)

	return cached.(Cols), nil
}

// collectSortable walks the model's fields, flattening embedded structs the way
// pgx does when it matches columns to fields.
func collectSortable(model reflect.Type, tagKey string, sortable Cols) {
	for i := range model.NumField() {
		field := model.Field(i)
		if len(field.PkgPath) > 0 && !field.Anonymous {
			continue
		}

		embedded := field.Type
		for embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}

		if field.Anonymous &&
			embedded.Kind() == reflect.Struct &&
			len(tagName(field, columnTagKey)) == 0 {
			collectSortable(embedded, tagKey, sortable)

			continue
		}

		column := tagName(field, columnTagKey)
		if column == "-" {
			continue
		}

		if len(column) == 0 {
			column = strings.ToLower(field.Name)
		}

		name := column
		if tagKey != columnTagKey {
			name = tagName(field, tagKey)
			if len(name) == 0 || name == "-" {
				continue
			}
		}

		sortable[strings.ToLower(name)] = column
	}
}

// tagName returns the name part of a struct tag: "name,omitempty" -> "name".
func tagName(field reflect.StructField, tagKey string) string {
	value, ok := field.Tag.Lookup(tagKey)
	if !ok {
		return ""
	}

	name, _, _ := strings.Cut(value, ",")

	return name
}
