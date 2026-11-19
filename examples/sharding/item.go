package main

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
)

type item struct {
	ID      int64  `db:"id"`
	Version int64  `db:"version"`
	Payload string `db:"payload"`
}

//go:embed schema.sql
var schemaSQL string

func writeItem(ctx context.Context, router *shard.Router[int64], c item) ([]shard.ShardResult, error) {
	return router.Write(ctx, c.ID, func(ctx context.Context, db *pgfx.DB) error {
		return db.Transaction(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) error {
			return saveItem(ctx, tx, c)
		})
	})
}

// saveItem demonstrates an application decision between two statements in a
// caller-owned transaction. If no Go-side decision is needed, the same operation
// can be expressed as one data-modifying CTE using RETURNING.
func saveItem(ctx context.Context, tx *pgfx.Tx, c item) error {
	const upsert = `
		INSERT INTO items (
			id,
			version,
			payload
		)
		VALUES (
			$1,
			$2,
			$3
		)
		ON CONFLICT (id) DO UPDATE
		SET
			version = excluded.version,
			payload = excluded.payload
		WHERE items.version < excluded.version
		RETURNING
			id,
			version,
			payload
	`

	_, isChanged, err := tx.FetchOptionalRow[item](
		ctx,
		upsert,
		c.ID,
		c.Version,
		c.Payload,
	)
	if err != nil {
		return err
	}

	if !isChanged {
		return nil
	}

	const record = `
		INSERT INTO item_history (
			id,
			version
		)
		VALUES (
			$1,
			$2
		)
	`

	_, err = tx.Exec(
		ctx,
		record,
		c.ID,
		c.Version,
	)
	if err != nil {
		return err
	}

	return nil
}
