//go:build integration

package pgfx_test

import (
	"os"
	"testing"
)

func TestDocumentationExamples(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	t.Run("optional row", func(t *testing.T) { ExampleDB_FetchOptionalRow() })
	t.Run("row buffer", func(t *testing.T) { ExampleDB_FetchRowsInto() })
	t.Run("value buffer", func(t *testing.T) { ExampleDB_FetchValuesInto() })
	t.Run("transaction value", func(t *testing.T) { ExampleDB_TransactionValue() })
	t.Run("page", func(t *testing.T) { ExampleDB_FetchPage() })
}
