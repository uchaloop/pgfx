package pgfx

import (
	"context"
	"testing"
)

func TestMakeDBValidConfigCreatesPool(t *testing.T) {
	// pgxpool.NewWithConfig does not dial until first use, so this succeeds
	// without a running database.
	cfg := Config{
		Host:     "127.0.0.1:5432",
		Database: "app",
		User:     "app",
		TLS:      TLSConfig{Mode: "disable"},
	}

	pool, err := MakeDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("MakeDB: %v", err)
	}

	if pool == nil {
		t.Fatal("MakeDB returned a nil pool")
	}

	pool.Close()
}

func TestMakeDBRejectsInvalidConfig(t *testing.T) {
	if _, err := MakeDB(context.Background(), Config{Database: "app"}); err == nil {
		t.Fatal("expected an error for a config without a host")
	}
}
