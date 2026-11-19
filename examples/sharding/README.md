# Sharding example

A runnable example of named Fx connections, a transaction that updates an item
and records its accepted version, and batch delivery through `Router.Partition`.
The SQL and item types belong to this example, not to the pgfx API.

## Run

Use two disposable PostgreSQL servers with the same database and credentials.
From the repository root:

```sh
export PGFX_TEST_HOST=127.0.0.1:55441
export PGFX_TEST_HOST_B=127.0.0.1:55442
export PGFX_TEST_DATABASE=pgfx_test
export PGFX_TEST_USER=postgres
export PGFX_TEST_PASSWORD=postgres
export PGFX_TEST_TLS_MODE=disable
go run ./examples/sharding
```

The program creates and retains the `pgfx_shard_example` schema in both
servers, with two tables from [schema.sql](schema.sql). It prints:

```text
item 0: succeeded=[a] failed=[]
item 1: succeeded=[b] failed=[]
```

Repeating the program is safe for its fixed full snapshots. Use dedicated
example databases; this is not a schema migration tool.

## Follow the code

- [fx.go](fx.go): supplies named configurations, registers `MakeModuleFor`,
  receives the unordered `group:"pgfx"` and builds a router by connection name.
  Constructors do not query the database. [main.go](main.go) starts Fx before
  executing SQL and stops it with an independent cleanup deadline.
- [item.go](item.go): `Router.Write` runs a transaction on each destination.
  A conditional upsert returns the accepted snapshot; only an accepted version
  adds a history entry. A history insert error rolls back the item update too.
  The two statements illustrate Go-side decisions inside `Transaction`; when
  no such decision is needed, use one data-modifying CTE with `RETURNING`.
- [batch.go](batch.go): groups original input with `Partition`, collapses each
  group's duplicate keys to the highest full-snapshot version, and writes the
  group in one transaction inside `Target.Attempt`. `defer attempt.Release()`
  also handles panic; normal completion reports `Done(err)` first.

Batch results have one entry per **original input position**. A result is fully
delivered only when every destination succeeds. Results remain available on
partial failure; an error before grouping returns no results. `Failed` means
unconfirmed, not necessarily unwritten: a lost COMMIT response is ambiguous.
Repeating the whole batch is safe under this example's version contract.
The group transaction makes one group's outcome apply to all its `Indexes`.
Superseded duplicates count as delivered by their newer full snapshot.
Equal versions must have identical payloads; conflicts within a batch are
rejected before any writes. Producers must enforce this across batches too.
Bound batch sizes and operation deadlines in the calling service.

`fx.go` configures two shards with non-overlapping bucket ranges. To add
shards, supply named configurations/modules and ranges covering all buckets. Enable `Config.Breaker` on every referenced connection.
Health shutdown is disabled here, so a shard outage cannot stop the application.
Startup is lazy; add `WithStartupPing()` when availability must be checked.
Queries issued directly through DB still need an `Attempt` to obey the breaker.

During a move, set a range's `Target` and increase the layout version.
`Write` and `Partition` then include both destinations. `Router.Update` validates
local transitions; it does not coordinate writers, copy data or fence stale
layout versions. See [the sharding contracts](../../README.md#sharding).

The history records versions accepted locally, not every input or a global
change log. Batch compaction and stale-version rejection can skip intermediate
versions. Each shard commits independently; the application owns retries and
cross-shard reconciliation.

## Verify

With the variables above, run:

```sh
go test -race -count=1 -timeout=2m -tags=integration -v ./examples/sharding
PGFX_SHARD_DOCKER_CONTAINER='your-disposable-container' \
  go test -race -count=1 -timeout=2m -tags=integration \
  -run '^TestShardDockerRecovery$' -v ./examples/sharding
```

The first command tests transactions, partial delivery and Fx with one pooled
connection per shard. The second command **stops and starts the specified Docker
container**. It must be a disposable server matching `PGFX_TEST_HOST_B`; never
point it at a shared database. It checks lazy startup, explicit startup Ping,
isolation of the healthy shard, breaker refusal and recovery of the same pool.
The tests create unique schemas and remove them afterwards. Do not run the
Docker outage test concurrently with other tests using those servers.

See [CONTRIBUTING](../../CONTRIBUTING.md) for the full library checks.
