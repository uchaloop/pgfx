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

// sortableCache holds the whitelist derived for a model and a tag. Deriving it
// is reflection over a type, and a type does not change.
var sortableCache sync.Map // sortableKey -> Cols

type sortableKey struct {
	model reflect.Type
	tag   string
}

// sortableOf derives the sort whitelist from a model's struct tags: the tag
// named by tagKey holds the name a client sends, the explicit db tag
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
	if cached, isOK := sortableCache.Load(key); isOK {
		return cached.(Cols), nil
	}

	sortable := make(Cols)
	if err := collectSortable(model, tagKey, sortable); err != nil {
		return nil, err
	}

	cached, _ := sortableCache.LoadOrStore(key, sortable)

	return cached.(Cols), nil
}

// collectSortable walks the model's fields, flattening embedded structs the way
// pgx does when it matches columns to fields.
func collectSortable(model reflect.Type, tagKey string, sortable Cols) error {
	for i := range model.NumField() {
		field := model.Field(i)
		if len(field.PkgPath) != 0 && !field.Anonymous {
			continue
		}

		// pgx flattens anonymous struct values even when the embedding has
		// a db tag (including "-"). Embedded pointers are ordinary fields.
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			if err := collectSortable(field.Type, tagKey, sortable); err != nil {
				return err
			}

			continue
		}

		column := tagName(field, columnTagKey)
		if column == "-" {
			continue
		}

		name := column
		if tagKey != columnTagKey {
			name = tagName(field, tagKey)
			if len(name) == 0 || name == "-" {
				continue
			}
		}

		if len(column) == 0 {
			return fmt.Errorf("%w: %s", ErrMissingColumnTag, field.Name)
		}

		key := strings.ToLower(name)
		if previous, isOK := sortable[key]; isOK && previous != column {
			return fmt.Errorf("%w: %q", ErrAmbiguousSortField, name)
		}

		sortable[key] = column
	}

	return nil
}

// tagName returns the name part of a struct tag: "name,omitempty" -> "name".
func tagName(field reflect.StructField, tagKey string) string {
	value, isOK := field.Tag.Lookup(tagKey)
	if !isOK {
		return ""
	}

	name, _, _ := strings.Cut(value, ",")

	return name
}
