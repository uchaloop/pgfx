package page_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/uchaloop/pgfx/page"
)

func TestOrderBy(t *testing.T) {
	sortable := page.Sortable[string]{
		"officeID": "o.id",
		"name":     "o.partner_name",
	}

	for _, tc := range []struct {
		name string
		keys []page.Key[string]
		want string
	}{
		{name: "default", want: "o.id ASC"},
		{name: "ascending", keys: []page.Key[string]{{Name: "name"}}, want: "o.partner_name ASC, o.id ASC"},
		{name: "descending", keys: []page.Key[string]{{Name: "name", Desc: true}}, want: "o.partner_name DESC, o.id DESC"},
		{name: "tie key", keys: []page.Key[string]{{Name: "officeID", Desc: true}}, want: "o.id DESC"},
		{
			name: "tie key first",
			keys: []page.Key[string]{{Name: "officeID", Desc: true}, {Name: "name"}},
			want: "o.id DESC, o.partner_name ASC",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sortable.OrderBy(tc.keys, "o.id")
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestOrderByRejectsKeys(t *testing.T) {
	sortable := page.Sortable[string]{"name": "o.partner_name"}

	for _, tc := range []struct {
		name string
		keys []page.Key[string]
	}{
		{name: "unknown", keys: []page.Key[string]{{Name: "missing"}}},
		{name: "duplicate", keys: []page.Key[string]{{Name: "name"}, {Name: "name", Desc: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sortable.OrderBy(tc.keys, "o.id")
			if !errors.Is(err, page.ErrSort) || !strings.Contains(err.Error(), tc.keys[len(tc.keys)-1].Name) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestOrderByRequiresTie(t *testing.T) {
	_, err := page.Sortable[string]{}.OrderBy(nil, "")
	if err == nil || errors.Is(err, page.ErrSort) {
		t.Fatalf("got %v", err)
	}
}

func TestOrderByNamedKey(t *testing.T) {
	type field string

	const createdAt field = "createdAt"

	got, err := page.Sortable[field]{createdAt: "created_at"}.OrderBy([]page.Key[field]{{Name: createdAt, Desc: true}}, "id")
	if err != nil || got != "created_at DESC, id DESC" {
		t.Fatalf("got %q, %v", got, err)
	}
}
