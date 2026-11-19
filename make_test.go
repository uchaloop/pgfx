package pgfx

import (
	"context"
	"testing"
)

func TestMakeDBRejectsInvalidConfig(t *testing.T) {
	if _, err := MakeDB(context.Background(), Config{Database: "app"}); err == nil {
		t.Fatal("expected an error for a config without a host")
	}
}

func TestMakeDBLazy(t *testing.T) {
	db, err := MakeDB(context.Background(), Config{Host: "127.0.0.1:1", Database: "x", TLS: TLSConfig{Mode: "disable"}})
	if err != nil {
		t.Fatalf("MakeDB: %v", err)
	}

	db.Close()
}

func TestMakeDBStartupPing(t *testing.T) {
	db, err := MakeDB(
		context.Background(),
		Config{Host: "127.0.0.1:1", Database: "x", TLS: TLSConfig{Mode: "disable"}},
		WithStartupPing(),
	)
	if err == nil || db != nil {
		t.Fatalf("MakeDB = %v, %v; want ping error", db, err)
	}
}
