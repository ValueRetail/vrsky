# AGENTS.md

A working reference for anyone changing this repository, human or agent.
How agents pick up issues, branch, review and merge here is in
[`CLAUDE.md`](CLAUDE.md); read that too if you are one.

VRSky is a multi-tenant integration platform: Go services on NATS JetStream
and PostgreSQL, a React UI, running on AKS in Azure. Production is
https://vrsky.valueretail.no. Payloads are not stored by the platform; they
move through JetStream, and large ones go to object storage with only a
reference on the bus (`src/pkg/claimcheck`, `src/pkg/objectstore`).

## Layout

| Path | What is there |
|---|---|
| `src/` | The Go module `github.com/ValueRetail/vrsky`. All Go commands run from here. |
| `src/cmd/<service>/` | One directory per binary, most with their own `Dockerfile`: `management-api` (control plane and REST API), `data-filter` and `data-converter`, the connectors (`webhook-consumer`, `http-producer`, `business-central-*`, `sitoo-*`, `sftp-*`, …), `remote-agent` (the gateway) and `vrsky-agent` (the agent that runs on customer machines), and the linters `lint-tenant-filter`, `lint-connector`, `lint-openapi`. |
| `src/pkg/` | Shared packages. The ones you meet first: `sdk` (the connector SDK; `sdk/harness` runs connectors against an embedded JetStream), `messaging` (NATS, streams, DLQ), `managementapi` (the API, the pipeline validator and the guard tests), `orchestrator`, `auth`, `testdb`. |
| `src/test/` | Contract and integration tests (`-tags=integration`). |
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
```

`make help` lists every target. The `Makefile` at the repo root forwards
`build`, `test`, `lint`, `fmt` and `vet` to `src/`, and owns the local-stack
targets (`up-core` and friends, below).

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

From the repo root, the same check CI runs:

```bash
docker run --rm -v "$PWD/infrastructure":/cfg --entrypoint promtool prom/prometheus:v3.5.0 check rules /cfg/prometheus-rules.yml
docker run --rm -v "$PWD/infrastructure":/cfg --entrypoint promtool prom/prometheus:v3.5.0 test rules /cfg/prometheus-rules-test.yml
```

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
`infrastructure/azure/affected.sh` maps the changed paths to images, those
images are built in ACR, the matching deployments roll by digest, and a
smoke check finishes the run. A change under `src/pkg` rebuilds every Go
image; one that touches only docs, plans or workflows deploys nothing.
Details: [`docs/operator/deploy.md`](docs/operator/deploy.md).

The cluster is parked (`az aks stop`) outside working hours: the Park
workflow stops it from 16:00 to 07:00 Oslo time, and it often stays stopped
longer. A merge while it is stopped builds its images and skips the rollout,
green, with a notice. The Production check (`drift-check.yml`, every half
hour) notices the difference once the cluster is running again and runs
the Catch up workflow, which brings production to `main`. See
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
