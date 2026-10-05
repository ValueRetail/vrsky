# Run the database tests that CI has been skipping

## Decisions (2026-10-05)

Include the `pkg/io` state-store tests.

## Built (2026-10-05) — result and deviations

- `pkg/testdb` has `Fresh(t)` (migrated) and `Empty(t)` (bare): the
  advisory-lock test and the state-store tests need a database, not the
  schema.
- All 19 state-store tests pass on a real Postgres — which also closes the
  one part of the pgx v5 move (#297) that had not been verified.
- **The agent isolation test did not cover groups** (`SetAgentGroups`,
  `ListAgentGroups` came later, in #283). Added. A first version of the
  check was masked by its own ordering: with the tenant condition removed,
  B's call still returned "not found" (the read-back is scoped) although it
  had rewritten A's row. The test now sets A's groups first and reads the
  row back, and the mutation fails it.
- The how-to went into `AGENTS.md` (the developer guide); there is no
  separate testing document.
- Verified on Linux under `-race` with the exact two CI steps before
  pushing, after the lesson of #297.

## Open questions

1. **Include the `pkg/io` state-store tests?** 19 tests behind the
   `integration` build tag exercise the (undeployed) Postgres worker's state
   store on a real database and no workflow runs them. Enabling them would
   also cover the one thing I could not verify in the pgx v5 move (#297).
   They *fail* rather than skip without a database, so they need the same
   helper. **Recommended: yes, as a second, separate step in this PR** — if
   they turn out to need more than a URL, they go to a follow-up instead of
   blocking the first part.

## Goal

Every test in the repo that needs a real management database runs on every
PR, and a developer can run the same tests locally with one variable.

## What I found (2026-10-05)

| Test | What it proves | Today |
|---|---|---|
| `pkg/managementapi` `TestWithAdvisoryLock_MutualExclusion` | two holders of the advisory lock exclude each other | skipped: `MGMT_TEST_DB_URL` unset |
| `pkg/managementapi` `TestAgentRepoDB_TenantScopingAndLifecycle` | tenant B cannot rename/revoke/regroup tenant A's agent **in Postgres**, not just in the query text | skipped |
| `cmd/remote-agent` `TestGatewayDB_RegistrationAndTenantBoundary` | a registration token is consumed once under a race; the agent lands in the token's tenant | skipped |
| `pkg/managementapi` `TestDriver_RepositoryBehaviourIsPinned` (#297) | driver behaviour snapshot | **runs** (own variable) |

- `MGMT_TEST_DB_URL` is set **nowhere**: not in a workflow, a Makefile or a
  doc. These three have never run in CI.
- **They pass today.** I ran them against a scratch database with all 25
  migrations: green alone, and green three times with both packages running
  in parallel, as `go test ./...` runs them.
- **Their cleanup never worked.** Each registers `t.Cleanup(DELETE …)` but
  also `defer db.Close()`, and deferred calls run before cleanups — so the
  deletes hit a closed pool and the error is discarded. Four runs left 16
  tenants, 8 users and 20 agents behind. Harmless on a throwaway database,
  wrong on a developer's.
- Two variables with different meanings already exist:
  `MGMT_TEST_DB_URL` = *an already-migrated database*;
  `MGMT_DRIVER_TEST_DB_URL` = *a server the test may create databases on*.

## Approach

### One helper, one variable

New `src/pkg/testdb` (test support, no production importers):

```go
// Fresh returns the URL of a new, empty database with every migration
// applied, dropped again when the test ends. It skips the test when
// VRSKY_TEST_POSTGRES_URL is not set.
func Fresh(t *testing.T) string
```

This is `freshDatabase` from the driver test, moved. `VRSKY_TEST_POSTGRES_URL`
is the URL of a Postgres **server** the tests may create databases on.

- The four tests call `testdb.Fresh(t)` and open their connection to it.
  Each gets its **own database**, so nothing is shared between tests or
  packages, there is nothing to clean up row by row, and the broken
  `t.Cleanup(DELETE …)` blocks are deleted rather than repaired.
- `MGMT_TEST_DB_URL` and `MGMT_DRIVER_TEST_DB_URL` go away; the comments
  that told people to set them are updated.
- Cost: ~0.3 s per test to create and migrate a database (25 small files).

### CI

`build-push.yml`, Go Tests job: rename the variable on the existing step to
`VRSKY_TEST_POSTGRES_URL`. The Postgres service is already there (#297).
No migration step in the workflow — the helper does it, so CI and a laptop
behave the same.

### A guard so this cannot silently regress

A test-skip is invisible. Add one check to the Go Tests step: after the run,
fail if the output contains `VRSKY_TEST_POSTGRES_URL not set` — in CI that
message can only mean the variable or the service was lost. (The run already
uses `-v`, so the skip line is in the log.)

### Local use

`docs/` gets a short "Database tests" note (and `make test-db` in
`src/Makefile`): start any Postgres, export
`VRSKY_TEST_POSTGRES_URL=postgres://…@localhost:5434/postgres?sslmode=disable`
(the dev stack's own Postgres works), run `go test ./...`.

### Step 2 — `pkg/io` state-store tests (open question 1)

`setupTestDB` takes its URL from `testdb.Fresh(t)` instead of a hard-coded
`localhost:5432/source_db`, and the Go Tests job runs
`go test -tags=integration ./pkg/io/ -run StateStore` as its own step. The
other `integration`-tagged tests in that package need NATS and a CDC source
and stay as they are.

## Files

| File | Change |
|---|---|
| `src/pkg/testdb/testdb.go` (+ test) | `Fresh` |
| `src/pkg/managementapi/driver_integration_test.go` | use the helper |
| `src/pkg/managementapi/dblock_test.go`, `repo_agents_db_test.go` | use the helper; drop dead cleanup |
| `src/cmd/remote-agent/gateway_db_test.go` | same |
| `.github/workflows/build-push.yml` | variable rename; skip guard; (step 2) state-store step |
| `src/pkg/io/postgres_state_store_test.go` | (step 2) helper |
| `src/Makefile`, `docs/` | `make test-db`, how to run them |

## Verification

- All four (and, with step 2, the 19 state-store tests) **run and pass**
  locally with the variable set, under `-race`, three times, with packages
  in parallel; and **skip** cleanly without it.
- No databases left on the server after a run (the helper's own test lists
  `pg_database`).
- The guard: run the CI step's script locally without the variable → it
  fails with the message.
- Mutations on the things these tests exist for, to show they bite now that
  they run: drop `AND tenant_id = $…` from `SetAgentGroups` → the agent
  test fails; drop `AND used_at IS NULL` from the token consumption → the
  gateway test fails.
- In CI: the Go Tests log shows `--- PASS` for each, not `--- SKIP`.

## Risks

- **A test that was never run in CI may be flaky there.** They were stable
  locally over repeated parallel runs; if one flakes in CI it is fixed, not
  re-skipped.
- `CREATE DATABASE` needs a role that may create databases — true for the CI
  service and the dev stack; the helper says so if it is not.
- Runtime: +1–2 s on the Go Tests job.

## Non-goals

The NATS/CDC `integration` tests in `pkg/io`; the nightly e2e; moving
sqlmock tests to a real database.
