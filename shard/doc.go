// Package shard routes rows to PostgreSQL shards inside the application.
//
// A key maps to one of a fixed number of buckets (Buckets), and a Layout maps
// bucket ranges to shards: named groups of pgfx connections with a master and
// optional replicas. Moving a bucket range between shards is the unit of
// resharding; the bucket count never changes, because changing it would move
// almost every row. Choose it large enough up front, for example 1024 buckets
// for up to about a hundred shards.
//
// The application owns where the layout comes from (an embedded file, ENV or
// a table) and when it changes; this package validates layouts, checks that a
// change is a permitted transition, and routes with one immutable snapshot at
// a time. It does not copy or verify data between shards and does not provide
// cross-shard transactions.
//
// Writes may partially succeed. Each callback must be safe to retry; for full
// snapshots use monotonically increasing per-key versions and conditional SQL
// updates. Equal versions must describe the same content. Deletes need versioned
// tombstones, and copying must follow the same version rule. A lost COMMIT
// response does not prove rollback. Layout transitions require application-level
// coordination of every writer and completion of old operations before cutover.
//
// Read and Shard.Read callbacks must be repeatable and have no external side
// effects. Replica reads may be stale and cannot claim or delete queue entries.
// The runnable examples/sharding program demonstrates transactions, Partition
// outcomes and Fx wiring without adding application policy to this package.
package shard
