package pgfx

import (
	"context"
	"testing"
)

func TestMakeValidConfigOpensPool(t *testing.T) {
	// pgxpool.NewWithConfig does not dial until first use, so this succeeds
	// without a running database.
	cfg := Config{Host: "127.0.0.1:5432", Database: "app", User: "app", TLS: TLSConfig{Mode: "disable"}}

	pool, err := Make(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	if pool == nil {
		t.Fatal("Make returned a nil pool")
	}
	pool.Close()
}

func TestMakeRejectsInvalidConfig(t *testing.T) {
	if _, err := Make(context.Background(), Config{Database: "app"}); err == nil {
		t.Fatal("expected an error for a config without a host")
	}
}
