//go:build integration

package page_test

import (
	"os"
	"testing"
)

func TestPageExamples(t *testing.T) {
	if len(os.Getenv("PGFX_TEST_HOST")) == 0 {
		t.Skip("PGFX_TEST_HOST is not set")
	}

	for _, tc := range []struct {
		name     string
		maxConns string
		run      func()
	}{
		{name: "sequential", maxConns: "1", run: ExampleFetch},
		{name: "parallel", maxConns: "2", run: ExampleFetch_parallel},
		{name: "transaction", maxConns: "1", run: ExampleFetch_transaction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGFX_TEST_MAX_CONNS", tc.maxConns)
			tc.run()
		})
	}
}
