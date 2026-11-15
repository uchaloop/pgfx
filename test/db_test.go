package pgfxtest

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMakeDBRequiresTestTarget(t *testing.T) {
	for _, missing := range []string{"PGFX_TEST_HOST", "PGFX_TEST_DATABASE"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("PGFX_TEST_HOST", "127.0.0.1:1")
			t.Setenv("PGFX_TEST_DATABASE", "test_db")
			t.Setenv("POSTGRES_HOST", "production.invalid")
			t.Setenv("POSTGRES_DATABASE", "production")
			t.Setenv(missing, "")
			db, err := MakeDB(context.Background())
			if db != nil {
				db.Close()
				t.Fatal("pool returned without test target")
			}

			if err == nil {
				t.Fatal("missing test configuration accepted")
			}
		})
	}
}

func TestMakeDBInvalidMaxConns(t *testing.T) {
	for _, value := range []string{"invalid", "-1", "2147483648", "1.5"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PGFX_TEST_HOST", "127.0.0.1:1")
			t.Setenv("PGFX_TEST_DATABASE", "test_db")
			t.Setenv("PGFX_TEST_MAX_CONNS", value)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			db, err := MakeDB(ctx)
			if db != nil {
				db.Close()
				t.Fatal("pool returned for invalid limit")
			}

			if err == nil || !strings.Contains(err.Error(), "PGFX_TEST_MAX_CONNS") {
				t.Fatalf("expected max connections validation error, got %v", err)
			}
		})
	}
}
