# Contributing

## Report a bug

Include the pgfx, Go and PostgreSQL versions, the expected and actual behavior,
and a minimal reproduction with the relevant schema and query. Remove credentials
and sensitive data from code, logs and connection settings.

## Submit a pull request

Keep the change focused and explain the problem it solves. Add a regression test
for bug fixes and tests for new behavior. Update examples and documentation when
public behavior changes, and record user-visible changes
under `Unreleased` in [CHANGELOG.md](CHANGELOG.md).

Run the local checks below and PostgreSQL tests for database-related changes.
Mention the checks you ran and any you could not run in the PR description.
Keep generated query plans, logs and local artifacts out of the repository.

Use the Go version declared in [go.mod](go.mod) and run commands from the repository
root. [CI](.github/workflows/ci.yml) checks formatting, module consistency, vet,
Staticcheck and race tests, including integration and restart tests on PostgreSQL 17.

## Local checks

```sh
export GOWORK=off
gofmt -l .
go mod tidy -diff
go vet ./...
go vet -tags=integration ./...
go install honnef.co/go/tools/cmd/staticcheck@v0.8.2
staticcheck ./...
staticcheck -tags=integration ./...
go test -count=1 -race -timeout=5m ./...
```

`gofmt -l` should print no files. Database examples in `example_test.go` are
compiled but require an `orders` schema to execute; examples with `Output`
comments run as part of `go test`. The standalone and Fx onboarding programs
are runnable against an empty database using the test variables below. The
README [Quick start](README.md#quick-start-with-fx) instead shows an application
that uses POSTGRES_* settings and runs until interrupted.

## PostgreSQL integration tests

Use a disposable database: the integration tests create and drop test tables.
For example, start a local PostgreSQL container and wait for readiness:

```sh
docker run --name pgfx-docs -d \
  -p 127.0.0.1:55432:5432 \
  -e POSTGRES_DB=pgfx_test -e POSTGRES_PASSWORD=postgres postgres:17-alpine
until docker exec pgfx-docs pg_isready -h 127.0.0.1 -U postgres -d pgfx_test; do sleep 1; done

export PGFX_TEST_HOST=localhost:55432
export PGFX_TEST_DATABASE=pgfx_test
export PGFX_TEST_USER=postgres
export PGFX_TEST_PASSWORD=postgres
export PGFX_TEST_TLS_MODE=disable
```

Then run:

```sh
go test -count=1 -race -timeout=5m -tags=integration ./...
go run ./examples/quickstart
go run ./examples/fx
```

Remove the disposable container when finished with all database checks:

```sh
docker rm -f -v pgfx-docs
```

The examples print `1`; the Fx program also logs lifecycle events. Without the
host variable, database tests skip. `TestPagePlans` additionally requires the
opt-in lab below and is skipped by the ordinary integration command. Do not
interpret a skipped test as a verified database scenario.

In package `shard`, only `TestRouterTwoInstancesIntegration` needs a second,
independent PostgreSQL server: set `PGFX_TEST_HOST_B` to it, with the same
database, user and password. Without it this test skips. It also expects
nothing to listen on `127.0.0.1:55435`, which stands for a shard that is down.
`TestWriteConvergesWithVersion` uses two schemas on the server configured by
`PGFX_TEST_HOST` and does not require `PGFX_TEST_HOST_B`. CI starts a second
service and checks with `jq` that both tests passed.

The [sharding example](examples/sharding/README.md) uses the same two-host
variables. Its tests create unique schemas and exercise the runnable example
code with `MaxConns=1`. `TestShardDockerRecovery` additionally requires
`PGFX_SHARD_DOCKER_CONTAINER`, naming the disposable container at
`PGFX_TEST_HOST_B`. It stops and starts that server, so run it separately from
other suites using the same servers:

```sh
PGFX_SHARD_DOCKER_CONTAINER=pgfx-example-b \
  go test -count=1 -race -timeout=2m -tags=integration \
  -run '^TestShardDockerRecovery$' -v ./examples/sharding
```

CI runs the example transaction, partial-success, Fx and Docker recovery tests
against its PostgreSQL services and checks each required test's `pass` event.

Tests using `pgfx/test` share the same connection variables and manage their own
fixtures. For use in your application, see [Testing](README.md#testing).
The integration suite also executes the optional-row, buffer-reuse,
transaction-value and page examples through `TestDocumentationExamples`.
They use `generate_series` and need no application tables. The
[page package examples](page/example_test.go) need no database and run with
plain `go test`.

## Additional checks

Run these when changing pagination performance or health-monitor behavior.

### Pagination-lab

You can run the [Pagination lab workflow](.github/workflows/pagination-lab.yml)
manually in GitHub Actions, or reproduce it locally with the commands below.
The workflow uploads query plans and results as the `pagination-lab` artifact,
available for seven days. Timings are informational, with no latency threshold.

This optional lab creates 2,000,000 synthetic rows with repeated `score` values,
text and JSONB payloads. The only index is the primary key on `id`, matching
the tested ID ordering; no secondary indexes on `score` are created.
Allow roughly 0.5 GB for the table and primary-key index, plus
PostgreSQL WAL and container storage. Use a fresh disposable cluster with no
existing `pagination_lab` role or database. The script intentionally fails
rather than replacing an existing lab.

With the `pgfx-docs` container from PostgreSQL integration tests above still running:

```sh
docker exec -i pgfx-docs psql -X -v ON_ERROR_STOP=1 -U postgres -d postgres \
  < scripts/pagination-lab.sql
mkdir -p /tmp/pgfx-page-plans
PGFX_PAGE_TEST_HOST=localhost:55432 \
PGFX_PAGE_BENCH=1 \
PGFX_PAGE_PLAN_DIR=/tmp/pgfx-page-plans \
  go test -count=1 -tags=integration -timeout=5m -run '^TestPagePlans$' -v .
```

The lab uses the local-only role, password and database `pagination_lab`, which
match the test helper. `PGFX_PAGE_TEST_HOST` is required; there is no fallback
to `PGFX_TEST_HOST`. `PGFX_PAGE_PLAN_DIR` must already exist. These credentials
and disabled TLS are only for the disposable lab.

The test compares two ways to read a page. `window` is one `FetchPage`
statement with `count(*) OVER ()`; `split` is `FetchRows` followed by a separate
`COUNT(*)`. Expected: six passing leaf subtests (`window/first`, `window/deep`,
`window/empty`, `split/first`, `split/deep`, `split/empty`) and nine EXPLAIN
artifacts: `page-window-{first,deep,empty}-page.json` and
`page-split-{first,deep,empty}-{rows,count}.json`.
With a limit of 20 and ID ascending, the cases cover offset 0, offset 1800000,
and offset 2000020 (past the end). Each verifies the returned list and
Total=2000000; the empty page must still return the total. Every statement is
explained. Plans are observations, not requirements for a particular scan type.

The test reports the median latency of one page with its total after warmup,
including queries, network and decoding in each mode. The window statement
evaluates every matching row before LIMIT applies, and a page past the end runs
it twice; compare both modes before choosing one for a large table.
Run performance checks without `-race`;
use the separate integration command for race checks. Measurements are not
cold-cache results or performance guarantees. Record PostgreSQL version,
hardware, cache conditions, indexes and dataset when comparing runs. Inspect
`Actual Rows`, buffer hits/reads, filters and sorts in the saved EXPLAIN ANALYZE
output. Deep offsets normally scan the skipped prefix; COUNT processes the
complete base result. NULLs, joins and selective filters need their own
representative measurements.

Remove the disposable container using the cleanup command in PostgreSQL integration tests above.

### Health monitor and breaker restart tests

Ordinary integration tests cover pool saturation, short failures, disabled
monitoring, named connections and stop cancellation, and the breaker's recovery
through an in-process TCP proxy. `TestHealthPostgresRestart` and
`TestBreakerPostgresRestart` are separately opt-in because it stops and restarts a PostgreSQL server. Use only
a dedicated disposable local cluster initialized with `initdb` and managed with
`pg_ctl`, with its socket/port set in `postgresql.conf` so plain `pg_ctl start`
reuses the same endpoint. Export the usual `PGFX_TEST_*` connection variables.
Then set `PGFX_HEALTH_PG_CTL` to the absolute path of `pg_ctl` and
`PGFX_HEALTH_PG_DATA` to that disposable cluster's data directory:

```sh
go test -count=1 -race -timeout=5m -tags=integration -run '^(TestHealthPostgresRestart|TestBreakerPostgresRestart)$' -v .
```

The health test verifies recovery after a brief real server outage and a
nonzero Fx shutdown signal after a prolonged outage. The breaker test stops the
server, fails operations until the breaker opens, starts the server, waits for
it independently of the breaker and checks that a probe closes the breaker. It leaves the test server
running so its owner can perform cleanup. Do not set these two variables for a
shared server or the Docker workflow above.
