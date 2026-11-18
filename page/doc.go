// Package page describes numbered pages: their bounds, their result and the
// sort keys a request may use. It depends only on the standard library, so
// request and domain types can use it without importing a database driver.
//
// MakeBounds turns a page number and size into LIMIT/OFFSET values.
// Sortable.OrderBy turns requested sort keys into an ORDER BY expression list
// built only from trusted SQL. github.com/uchaloop/pgfx.DB.FetchPage runs the
// page query and returns a Result.
package page
