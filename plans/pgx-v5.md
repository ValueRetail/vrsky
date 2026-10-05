# Move off pgx v4 (GO-2026-5004, GO-2026-4518)

## Decisions (2026-10-05)

Migrate the two `pkg/io` workers too; correct the allow-list note first.

## Built (2026-10-05) — result and deviations

- **No behaviour change found.** The driver test recorded a snapshot on pgx
  v4 and passes unchanged on v5 (three stable runs on v4 first). The code
  diff is imports plus `pgxpool.Connect` → `pgxpool.New`; nothing else had
  to move.
- The test compares a **recorded snapshot** (`testdata/driver_snapshot.json`,
  what the API would serialise, ids and database-stamped times normalised)
  rather than hand-written assertions: any difference in type, zone,
  precision, NULL, array or error class is a diff. `time.Local` is pinned to
  a fixed non-UTC zone in the test so "returns Local" and "returns UTC"
  cannot be confused.
- CI: the Go Tests job gets a `postgres:16-alpine` service and
  `MGMT_DRIVER_TEST_DB_URL`; without the variable the test skips.
- Dev-stack walk-through done through the HTTP API on the rebuilt local
  management-api (auth, pipelines incl. the 409 on a duplicate name, agents
  and groups, notification targets, audit, API key): all as before, no
  error logs.

## Open questions

1. **Migrate or delete the two `pkg/io` Postgres workers?**
   `cmd/postgres-consumer` (CDC) and `cmd/postgres-producer` are not built as
   product images, not deployed, and not reachable from any pipeline (#239);
   they exist as docker-compose load fixtures (~2 000 lines in
   `pkg/io/postgres_{input,output}.go`). Their pgx surface is tiny (4
   identifiers). **Recommended: migrate them in the same PR** — cheaper than
   settling what they are for, and it lets pgx v4 leave `go.mod` completely.
   Deleting them is the alternative and a product decision.
2. **Fix the allow-list note now, separately?** It is wrong today (see below).
   Recommended: yes, as the first commit of this work, so the file is true
   even if the migration takes a few days.

## What I found (2026-10-05) — the allow-list note is wrong

`src/.govulncheck-allow` says both advisories are denial-of-service issues
reachable "only through `pkg/io/postgres_input.go`", a worker prod does not
run. Scanning each binary separately with CI's toolchain shows otherwise:

| Binary | Reaches GO-2026-5004 / GO-2026-4518 |
|---|---|
| `cmd/management-api` (**prod, the core API**) | **yes** — its database driver is `github.com/jackc/pgx/v4/stdlib` (`sql.Open("pgx", …)`) |
| `cmd/postgres-consumer`, `cmd/postgres-producer` | yes (pgx v4 pool) |
| every connector (`db-consumer`, `db-producer`, …) | no — they use `lib/pq` |

And GO-2026-5004 is not a DoS: it is **SQL injection via placeholder
confusion with dollar-quoted string literals** in pgx's client-side query
sanitiser. GO-2026-4518 is a DoS in `pgproto3` when reading server messages.
Both are "Fixed in: N/A" for v4; pgx v5 (latest v5.11.0) is the fix.

**Is prod exploitable today? My assessment: no, but it is reasoning, not
proof.** The sanitiser is only used in *simple protocol* mode; the management
API uses `database/sql` with pgx's default extended protocol, no
`prefer_simple_protocol` anywhere in the repo, and no dollar-quoted SQL in its
queries. The pgproto3 DoS needs a malicious or compromised Postgres server on
the other end, and prod talks to its own in-cluster database. That is why
this is "do it properly", not "drop everything" — but the real driver of the
core API should not sit on a module line with no fixes.

## Goal

No pgx v4, `pgproto3/v2` or `pgconn` v1 in `go.mod`; `src/.govulncheck-allow`
has no entries; the management API behaves identically on pgx v5.

## What changes

| Where | Today | After |
|---|---|---|
| `cmd/management-api/main.go` | `_ "github.com/jackc/pgx/v4/stdlib"` | `_ "github.com/jackc/pgx/v5/stdlib"` (driver name stays `pgx`) |
| `pkg/managementapi/postgres_repository.go` | `github.com/jackc/pgconn` (`*pgconn.PgError`, code 23505) | `github.com/jackc/pgx/v5/pgconn` |
| `pkg/io/postgres_input.go`, `postgres_output.go` | `pgx/v4`, `pgxpool.Connect` | `pgx/v5`, `pgxpool.New` |
| `pkg/io/postgres_state_store_test.go` | `pgx/v4/stdlib` | `pgx/v5/stdlib` |
| `src/.govulncheck-allow` | two entries, wrong reason | empty (header kept) |
| `.github/workflows/vuln-scan.yml` | comment about two accepted advisories | updated |

Everything else that talks to Postgres uses `lib/pq` and is untouched.

## The risk, and how it is contained

The driver under **every query the core API makes** changes. Unit tests use
sqlmock and never touch a driver, so they cannot tell v4 from v5. What can
differ between the two `stdlib` drivers:

- how typed scans behave for the column types the API uses (it never scans
  into untyped `any` through this driver — checked — so the exposure is the
  typed cases below, not arbitrary values);
- `pq.Array` on `TEXT[]` (14 uses: agent groups, token groups, …) — a
  `lib/pq` helper riding on the pgx driver;
- JSONB in and out as `json.RawMessage` / `[]byte` (40 columns), `INET`
  (sessions), `UUID` into `string` (78 columns), NULL handling;
- error values: `*pgconn.PgError` must be the v5 type or the unique-violation
  check silently stops matching (the string-based checks elsewhere keep
  working);
- statement caching is on by default in v5's stdlib.

**Approach: prove equivalence before switching.**

1. Write `pkg/managementapi/driver_integration_test.go`. It needs a real
   Postgres; no CI job gives the Go tests one today (the Connector
   integration job starts brokers, not a database), so the "Go Tests" job
   gets a `postgres:16-alpine` service and the test reads its URL from
   `MGMT_DRIVER_TEST_DB_URL`, skipping itself when that is unset (the pattern
   the other integration tests use). The test applies the migrations and
   round-trips each case above through `sql.Open("pgx", …)` **and through the
   real repository methods**: UUID, JSONB, `TEXT[]` via `pq.Array`, INET,
   TIMESTAMPTZ, NULLs, `RETURNING`, mixed-type query arguments, and a unique
   violation (the typed `PgError` check and the string checks).
2. Run it on **v4 first**. It must pass; that pins today's behaviour.
3. Switch the imports to v5. The same test must pass unchanged. Any
   difference is either fixed in code (explicit scan types) or, if v5's
   answer is the better one, called out in the PR.
4. Run the repository's real-Postgres flows: the `pkg/io` integration tests,
   the DR backup/restore drill and the webhook→HTTP smoke (both run on PRs),
   and locally the dev stack: log in, create and deploy a pipeline, agents
   page with groups, notification targets, audit log, API key.

## Files

| File | Change |
|---|---|
| `src/.govulncheck-allow`, `.github/workflows/vuln-scan.yml` | step 0: correct the note; last step: remove the entries |
| `src/pkg/managementapi/driver_integration_test.go` (new) | the equivalence test |
| `src/cmd/management-api/main.go`, `src/pkg/managementapi/postgres_repository.go` | imports |
| `src/pkg/io/postgres_input.go`, `postgres_output.go`, `postgres_state_store_test.go` | pgx v5 API |
| `src/go.mod`, `src/go.sum` | pgx v5 in, v4 / pgconn v1 / pgproto3 v2 / pgtype v1 / puddle v1 out |
| `.github/workflows/build-push.yml` | Postgres service + `MGMT_DRIVER_TEST_DB_URL` for the Go Tests job |

## Verification

- The equivalence test: green on v4, then green on v5 without edits.
- `govulncheck` with CI's toolchain, whole repo **and per binary**: no
  findings; allow-list empty; the workflow's stale-entry check passes.
- `go mod why` shows no path to `pgx/v4`.
- `gofmt`, `go vet`, `golangci-lint`, `lint-tenant`, `go test -race ./...`,
  the new driver test against a real Postgres locally and in CI, and the
  existing `pkg/io` integration tests.
- Dev stack walk-through (above).

## Rollout

`build-push-acr.sh core` → `deploy-core-azure.sh management-api`. Watch
`/readyz` (database: ok), log in, open the builder and Settings → Remote
agents (the `TEXT[]` path), check the API error rate alert stays quiet.
Rollback is the previous image digest (`kubectl rollout undo`); there is no
schema change, so rolling back is safe at any point.

## Non-goals

Moving the connectors from `lib/pq` to pgx; using pgx natively (pgxpool) in
the management API; deciding the future of the CDC worker (open question 1).
