package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/secret/v2"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := pgfx.Config{
		Host:     os.Getenv("PGFX_TEST_HOST"),
		Database: os.Getenv("PGFX_TEST_DATABASE"),
		User:     os.Getenv("PGFX_TEST_USER"),
		Password: secret.New(os.Getenv("PGFX_TEST_PASSWORD")),
		TLS:      pgfx.TLSConfig{Mode: "disable"}, // Local disposable database only.
	}
	db, err := pgfx.MakeDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// MakeDB creates the pool; Ping verifies connectivity.
	if err := db.Ping(ctx); err != nil {
		return err
	}

	value, err := db.FetchValue[int](ctx, "SELECT 1")
	if err != nil {
		return err
	}

	fmt.Println(value)

	return nil
}
