package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/uchaloop/pgfx"
	"github.com/uchaloop/pgfx/shard"
)

type itemResult struct {
	Succeeded []string
	Failed    []string
}

// writeBatch returns one result per input position, including duplicate keys.
// An item is complete only when Failed is empty AND the overall result exists.
func writeBatch(ctx context.Context, router *shard.Router[int64], items []item) ([]itemResult, error) {
	// Reject conflicting snapshots before writing to any destination.
	if _, err := latestItems(items); err != nil {
		return nil, err
	}

	partition, err := router.Partition(items, func(c item) int64 { return c.ID })
	if err != nil {
		return nil, err
	}

	results := make([]itemResult, len(items))
	var failures []error

	for _, group := range partition.Groups {
		err := writeGroup(ctx, group)
		for _, index := range group.Indexes {
			if err != nil {
				results[index].Failed = append(results[index].Failed, group.Target.Shard)
				continue
			}

			results[index].Succeeded = append(results[index].Succeeded, group.Target.Shard)
		}

		if err != nil {
			failures = append(failures, fmt.Errorf("write %s: %w", group.Target.Shard, err))
		}
	}

	// Partial results are intentional: callers need them even when one group fails.
	return results, errors.Join(failures...)
}

func writeGroup(ctx context.Context, group shard.Group[item]) error {
	items, err := latestItems(group.Items)
	if err != nil {
		return err
	}

	ctx, attempt, err := group.Target.Attempt(ctx)
	if err != nil {
		return err
	}

	defer attempt.Release()

	// All items and history entries in this destination group commit or roll back together.
	err = group.Target.DB.Transaction(ctx, pgx.TxOptions{}, func(tx *pgfx.Tx) error {
		for _, c := range items {
			if err := saveItem(ctx, tx, c); err != nil {
				return err
			}
		}

		return nil
	})
	attempt.Done(err)
	if err != nil {
		return err
	}

	return nil
}

// latestItems is valid for complete snapshots only, never increments or deltas.
// Equal versions must describe identical content, including superseded versions.
func latestItems(items []item) ([]item, error) {
	type revision struct{ id, version int64 }

	payloads := make(map[revision]string)
	indexes := make(map[int64]int)
	latest := make([]item, 0, len(items))

	for _, c := range items {
		rev := revision{id: c.ID, version: c.Version}
		if payload, isFound := payloads[rev]; isFound && payload != c.Payload {
			return nil, errors.New("conflicting snapshots with the same version")
		}

		payloads[rev] = c.Payload
		index, isFound := indexes[c.ID]
		if !isFound {
			indexes[c.ID] = len(latest)
			latest = append(latest, c)
			continue
		}

		if c.Version > latest[index].Version {
			latest[index] = c
		}
	}

	return latest, nil
}
