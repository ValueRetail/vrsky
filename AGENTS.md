# AGENTS.md

A working reference for anyone changing this repository, human or agent.
How agents pick up issues, branch, review and merge here is in
[`CLAUDE.md`](CLAUDE.md); read that too if you are one.

VRSky is a multi-tenant integration platform: Go services on NATS JetStream
and PostgreSQL, a React UI, running on AKS in Azure. Production is
https://vrsky.valueretail.no.

Payloads move through JetStream, and large ones go to object storage with
only a reference on the bus (`src/pkg/claimcheck`, `src/pkg/objectstore`).
What the platform does keep:

- each consumer writes the latest payload of a connection to
  `connections.last_payload` (at most every 30 seconds,
  `src/pkg/sdk/lifecycle.go`) so the UI can show sample data;
- a message that fails every delivery attempt lands in the DLQ stream and
  stays there for 7 days (`messaging.DLQRetention`);
- a large payload offloaded to object storage stays for 8 days
  (`messaging.SpillRetention`), a day longer than the DLQ;
- the main stream (`VRSKY_DATA`) keeps every message, acked or not, for
  up to 72 hours (`messaging.MainRetention`), or until its 512 MiB /
  1M-message caps shed the oldest. An ack deletes nothing.

## Layout

| Path | What is there |
|---|---|
| `src/` | The Go module `github.com/ValueRetail/vrsky`. All Go commands run from here. |
| `src/cmd/<service>/` | One directory per binary, most with their own `Dockerfile`: `management-api` (control plane and REST API), `data-filter` and `data-converter`, the connectors (`webhook-consumer`, `http-producer`, `business-central-*`, `sitoo-*`, `sftp-*`, …), `remote-agent` (the gateway) and `vrsky-agent` (the agent that runs on customer machines), and the linters `lint-tenant-filter`, `lint-connector`, `lint-openapi`. |
| `src/pkg/` | Shared packages. The ones you meet first: `sdk` (the connector SDK; `sdk/harness` runs connectors against an embedded JetStream), `messaging` (NATS, streams, DLQ), `managementapi` (the API, the pipeline validator and the guard tests), `orchestrator`, `auth`, `testdb`. |
| `src/internal/config/` | An old environment-config loader. Nothing in the module imports it. |
| `src/test/` | `contract/` is the shared machinery for the connector contract tests, and `fixtures/` holds their recorded payloads. The `*_contract_test.go` files themselves live in the connector `cmd/` directories and run in a plain `go test`. Only `integration/` is behind `-tags=integration`. |
| `ui/` | The admin UI: React 18, TypeScript, Vite, Tailwind, Vitest. |
| `infrastructure/migrations/` | SQL migrations. The management API applies them when it starts. |
| `infrastructure/kubernetes/` | Manifests and install scripts for the cluster: core services, connectors, monitoring, ingress, NATS, Postgres, MinIO. |
| `infrastructure/azure/` | The scripts the deploy job and operators run: image builds in ACR, rollouts, smoke check, drift check, budget. |
| `infrastructure/prometheus-rules.yml` | Alert rules, with unit tests in `prometheus-rules-test.yml`. |
| `docs/` | The documentation site (MkDocs): operator runbooks in `docs/operator/`, connector pages in `docs/connectors/`, ADRs in `docs/adr/`. |
| `plans/` | One `plans/<task>.md` per non-trivial change, linked from its PR. `plans/backlog.md` holds work without an issue yet. |
| `docker-compose.yml` | The local stack. |

## Tools

Go 1.26 (CI takes the newest patch), golangci-lint v2.13.2 (configured in
`src/.golangci.yml`), Node 20 for the UI, and Docker for the local stack
and the promtool check.

## Go

From `src/`:

```bash
go build ./...                # or `make build`, which writes every binary to ./bin
go vet ./...
gofmt -l .                    # CI fails if this prints anything; fix with gofmt -w <file>
golangci-lint run
go test -race ./...           # CI runs go test -v -race -cover ./...
make lint-tenant              # see "Tenant isolation"
make lint                     # lint-tenant, lint-connector, lint-openapi and golangci-lint
govulncheck ./...             # when dependencies change; the nightly scan runs it too
make build-agent              # cross-compiles vrsky-agent for Windows, Linux and macOS into ./bin
```

`make help` in `src/` lists the targets there. The `Makefile` at the repo
root forwards every one of them to `src/`, and owns the local-stack targets
(`up-core`, `up-core-remote`, `down-core`, below); its `make help` lists
both. `src/pkg/managementapi/makefile_guard_test.go` fails if a root
forward or a help entry points at a target that doesn't exist, or if root
`make help` stops listing a documented target.

New management-API routes need an entry in
`src/pkg/managementapi/openapi_registry.go`; `make lint-openapi` fails
without one. Connectors build on `src/pkg/sdk` and call `sdk.Run*`;
`make lint-connector` checks that.

### Tests that need a real Postgres

Most tests run on sqlmock. A few need a real database: tenant isolation as
Postgres enforces it, the advisory lock, agent registration under a race,
the database driver's behaviour, and the `pkg/io` state store. They use
`src/pkg/testdb`: each creates its own database, applies the migrations,
and drops it afterwards. Without `VRSKY_TEST_POSTGRES_URL` they skip, so a
plain `go test ./...` still passes; CI sets it and fails if one is skipped
there.

From `src/`:

```bash
# Any Postgres the role may create databases on; the local stack's
# postgres-management (port 5434) works:
export VRSKY_TEST_POSTGRES_URL="postgres://postgres:<password>@localhost:5434/postgres?sslmode=disable"
make test-db                  # the core set: testdb, advisory lock, agent registration, driver, pkg/io state store
go test -race ./...           # every other testdb user too (quotas, login, billing, idempotency, SDK restore),
                              # but not the pkg/io state store: that one is behind -tags=integration
```

### Tests that reach the public internet

Three tests in `pkg/io` call the live https://api.met.no API. They skip
unless `VRSKY_TEST_LIVE_APIS` is set. CI never sets it, so a slow public
service cannot fail a merge or block a deploy. `TestLiveAPITestsAreOptIn`
keeps every live test behind that skip and the host out of every other test
file.

From `src/`:

```bash
VRSKY_TEST_LIVE_APIS=1 go test ./pkg/io/ -run TestRealAPI -v
```

### Connector integration tests

`.github/workflows/integration.yml` runs on every PR. It starts the test
brokers from `docker-compose.yml` (`minio-test`, `minio-init`,
`azurite-test`, `fake-gcs-test`, `sftp-test`, `kafka-test`,
`rabbitmq-test`) and runs the `-tags=integration` tests in
`pkg/objectstore`, `cmd/sftp-consumer`, `cmd/kafka-consumer` and
`cmd/rabbitmq-consumer`. Each test skips unless its `*_TEST_*` variable is
set, so a plain `go test` passes over them.

Kafka and RabbitMQ publish no host ports, and fake-gcs advertises itself
as `fake-gcs-test:4443`, so run the tests the way CI does: in a container
on `vrsky-network`. From the repo root:

```bash
docker compose up -d minio-test minio-init azurite-test fake-gcs-test sftp-test kafka-test rabbitmq-test
docker run --rm --network vrsky-network -v "$PWD/src":/src -w /src \
  -e S3_TEST_ENDPOINT=http://minio-test:9000 -e S3_TEST_ACCESS_KEY=minioadmin -e S3_TEST_SECRET_KEY=minioadmin \
  -e AZURE_TEST_CONN="DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://azurite-test:10000/devstoreaccount1;" \
  -e GCS_TEST_EMULATOR_HOST=fake-gcs-test:4443 \
  -e SFTP_TEST_HOST=sftp-test -e SFTP_TEST_PORT=22 \
  -e KAFKA_TEST_BROKER=kafka-test:9092 \
  -e RABBITMQ_TEST_URL=amqp://guest:guest@rabbitmq-test:5672/ \
  -e GOFLAGS=-buildvcs=false \
  golang:1.26 go test -tags=integration -count=1 ./pkg/objectstore/... ./cmd/sftp-consumer/... ./cmd/kafka-consumer/... ./cmd/rabbitmq-consumer/...
docker compose stop minio-test azurite-test fake-gcs-test sftp-test kafka-test rabbitmq-test
```

The Azure key is Azurite's published development key, not a secret. Give
the brokers a few seconds after `up` (Kafka is the slowest); CI waits for
each port before it starts.

### Guard tests

`src/pkg/managementapi/nodeconfig_test.go` holds tests that read files
across the repo and fail when they drift apart: every connector type the
UI offers has a validator rule and a deployed service, every image is
built, ingress points at services that exist, the deploy job waits for the
tests and skips a stopped cluster, the Park and drift-check workflows behave,
CPU requests and disk sizes stay within budget, and the scripts that run
inside the cluster use only the tools it has. When you change a workflow,
a manifest, a Dockerfile or an `infrastructure/azure` script, expect one of
these to have an opinion.

Some of them run shell scripts through bash, and `go test` does not see
the files those scripts read, so a cached pass can survive a change. Run
them with `-count=1`, always when mutation-testing a guard. From `src/`:

```bash
go test -count=1 ./pkg/managementapi/                         # the whole package, about a minute
go test -count=1 ./pkg/managementapi/ -run 'TestParkWorkflow' # one guard
```

### Tenant isolation

Every read, update or delete on a tenant-scoped table filters by
`tenant_id`. `make lint-tenant` (from `src/`) checks this statically over
the management API, the remote-agent gateway and the webhook consumer, and
CI runs it. A query that legitimately crosses tenants (an admin job, say)
carries a `lint:tenant-ok` comment saying why. A new table with a
`tenant_id` column goes into the list in `src/cmd/lint-tenant-filter`.

Tenant isolation as Postgres enforces it is tested through `src/pkg/testdb`
(above). Changes to auth, tenancy or secrets handling are never merged by an
agent; see `CLAUDE.md`.

## UI

From `ui/`:

```bash
npm ci
npm run dev                   # http://localhost:5173, proxies /api to the management API on :3000
npm run lint
npm run build                 # tsc -b, then vite build
npx vitest run --coverage     # same as npm run test:coverage, which CI runs
```

Run `npm run build` before pushing, not only `tsc --noEmit`: `tsc -b` catches
errors (`erasableSyntaxOnly`, for one) that `--noEmit` misses.

Coverage thresholds live in `ui/vitest.config.ts` and CI enforces them:
lines, functions and statements 80%, branches 75%.

## Alert rules

From the repo root, the same checks CI runs (CI splits `test rules` over
four legs with `--run`; locally the whole file takes about 90 s):

```bash
docker run --rm -v "$PWD/infrastructure":/cfg:ro --entrypoint promtool prom/prometheus:v3.5.0 check rules /cfg/prometheus-rules.yml
docker run --rm -v "$PWD/infrastructure":/cfg:ro --entrypoint promtool prom/prometheus:v3.5.0 test rules /cfg/prometheus-rules-test.yml
```

A new test group in `prometheus-rules-test.yml` needs a `name:` made of
`[a-z0-9-]`, and that name must be listed in exactly one leg of the
`alert-rules` matrix in `.github/workflows/build-push.yml`. A group no leg
names never runs in CI. `TestRuleTestGroupsRunOnce` fails until it is listed.
To run one group: `--run '^<name>$'`.

## Docs site

`docs/` is built with MkDocs (`mkdocs.yml` at the root) and published to
GitHub Pages from `main`. CI builds it with `--strict`, so a broken link
fails the PR. From the repo root, with `pip install mkdocs-material`:

```bash
mkdocs build --strict
```

## Local stack

From the repo root:

```bash
make up-core                  # the 12 services a pipeline needs end to end; fits a laptop with 8 GB
docker compose up -d --build  # the full stack (much heavier)
docker compose ps
docker compose logs -f management-api
make down-core                # stops the core set
docker compose down           # stops everything
```

The management API answers on http://localhost:3000, NATS on 4222, its
Postgres (`postgres-management`) on 5434, and httpbin (the sink a first
pipeline's HTTP destination points at) on 8080. Everything binds to
127.0.0.1. `make up-core-remote` exposes the webhook ingress to other
machines; see `docker-compose.remote.yml`.

## How a merge deploys

A merge to `main` is a production deploy. `.github/workflows/build-push.yml`
runs the Go tests, the UI tests with coverage, the remote-agent builds and
the Windows agent tests on the merge commit. When they are green,
`infrastructure/azure/affected.sh` maps the changed paths to targets and
the affected images are built in ACR. Then:

- core services (`management-api`, `ui`, `data-filter`, `data-converter`,
  `alert-responder`) roll by digest through `deploy-core-azure.sh`;
- a changed connector gets a `kubectl rollout restart`, or the whole fleet
  goes through `deploy-connectors-azure.sh` when the connector manifest
  changed;
- a change to `infrastructure/prometheus-rules.yml` or
  `infrastructure/kubernetes/monitoring/` runs `install-monitoring.sh`;
- a smoke check runs; after it, every deploy refreshes the `acr-pull`
  copies in `vrsky-database` and `vrsky-ui` (`copy-acr-pull.sh`), and a backup change
  applies the backup CronJob (`deploy-backup-azure.sh`).

A change under `src/pkg` (or `src/go.mod`, or any `src/cmd/*/Dockerfile`)
rebuilds and restarts every Go service; one that touches only docs, plans
or workflows deploys nothing. Details:
[`docs/operator/deploy.md`](docs/operator/deploy.md).

The cluster is parked (`az aks stop`) outside working hours. Any Park run
between 16:00 and 07:00 Oslo time stops a running cluster, but GitHub
fires them late, so expect the stop somewhere between 16:00 and 20:00. It
often stays stopped longer. A merge while it is stopped builds its images
and skips the rollout, green, with a notice. The Production check
(`drift-check.yml`, every half hour) notices the difference once the
cluster is running again and runs the Catch up workflow, which brings
production to `main`. See
[`docs/operator/cost-parking.md`](docs/operator/cost-parking.md).

Revert by reverting the merge in a PR; the revert deploys like any change.

## Go conventions

- Effective Go, gofmt-clean. Imports in three groups: standard library,
  third-party, then `github.com/ValueRetail/vrsky/...`.
- Short lowercase package names; file names in `snake_case.go`; acronyms
  keep their case (`HTTPProducer`, `tenantID`, `apiURL`).
- Check errors right away and wrap them with context:
  `fmt.Errorf("load connection %s: %w", id, err)`. Sentinel errors for
  domain cases (`var ErrNotFound = errors.New(...)`). An ignored error gets
  `_ =` and a reason.
- Exported identifiers have doc comments. Comments explain why, and the
  existing code leans on that: say which issue or incident a rule comes from.
- Pass a `context.Context` through anything that blocks, and stop
  goroutines when it is cancelled.
- Don't write your own retry loop around a delivery. A handler that fails
  returns an error and JetStream redelivers with backoff
  (`messaging.DefaultBackoff`, `src/pkg/messaging/subscriber.go`), up to
  `messaging.MaxDeliveryAttempts` (5, `src/pkg/messaging/messaging.go`).
  After that the message moves to the DLQ. In the SDK, wrap an error that
  retrying can't fix in `sdk.Permanent`: the message is acked and logged
  instead of retried, and it does not go to the DLQ.
- Tests are table-driven where there is more than one case. NATS behaviour
  is tested against the embedded server in `src/pkg/sdk/harness`, not only
  mocks; database behaviour that Postgres decides goes through
  `src/pkg/testdb`.
- Log with structured fields: tenant, connection and message ID where
  they exist.

## Commits and PRs

- Branch from `main`: `feat/…`, `fix/…`, `docs/…`, `ci/…`, `chore/…`,
  `test/…`, or `issue-<n>-<slug>` for agent work.
- [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): subject`, for example
  `fix(builder): a redeploy keeps the pipeline's name and description`.
- A non-trivial change gets `plans/<task>.md` (goal, acceptance criteria,
  files, approach, how it is verified), linked from the PR.
- The PR says what changed, the decisions made on the way, and the test
  evidence. "Closes #n" when it closes an issue. Squash-merged.
- Behaviour or configuration changes update `docs/` in the same PR.
