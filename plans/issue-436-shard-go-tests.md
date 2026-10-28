# #436: shard the Go tests

## Open questions

- None blocking. Branch protection on `main` is off (`gh api
  repos/ValueRetail/vrsky/branches/main/protection` → 404, no rulesets), so
  no required check pins the name "Go Tests". We keep an aggregator job with
  that name anyway: one result to look at, and ready if protection is
  turned on later.

## Goal

The single Go Tests job took 15 m 26 s on run 40684491716 (12:01:42 →
12:17:08), and every image build waited for it. Make it a lot shorter
without dropping a package, `-race`, the Postgres tests or the
skipped-DB-test check.

## Where the time went (run 40684491716, job 122803242589)

| Step | Time |
|---|---|
| Setup, containers, checkout, promtool, setup-go | ~38 s |
| `go test -v -race -cover ./...` | 12 m 31 s |
| `pkg/io` state-store tests | 5 s |
| golangci-lint | 2 m 03 s |
| lint-openapi, lint-tenant, post steps | ~2 s |

Per package (test time as `go test` reports it): `pkg/managementapi` 504 s,
`pkg/messaging` 48 s, `cmd/remote-agent` 46 s, `cmd/webhook-consumer` 28 s,
`pkg/sdk` 21 s, `cmd/file-consumer` 19 s, everything else 10 s or less.

Inside `pkg/managementapi`, two tests are 380 of the 504 s:
`TestChangePassword_SuccessRefunds` (221 s) and `TestAuthLimitsFromEnv`
(160 s), both bcrypt at production cost. #406 (open) moves them to
`bcrypt.MinCost`. Sharding can't split one package's sequential tests, so
until #406 lands the `managementapi` shard sets the floor at roughly 9–10
minutes; after it, all shards should sit within a few minutes.

## Approach

`.github/workflows/build-push.yml`:

1. `go-test-shards` (shown as "Go Tests (<shard>)"): a matrix over
   `shard: [managementapi, messaging, agents, rest]`, `fail-fast: false`.
   Each leg has the Postgres service, the retried module download,
   `GOPROXY=off`, `VRSKY_TEST_POSTGRES_URL`, `go test -v -race -cover` on its
   packages, and the grep that fails the job on a skipped DB test. Database
   tests live in six packages across shards, so every leg gets the service
   and the check rather than tracking which ones need it.
2. `.github/scripts/go-test-shards.sh <shard>` prints a shard's packages.
   Named shards list the slow packages by exact path (not `/...`, so
   `pkg/sdk/harness` stays small and in `rest`):
   - `managementapi`: `pkg/managementapi`
   - `messaging`: `pkg/messaging`, `pkg/sdk`
   - `agents`: `cmd/remote-agent`, `cmd/webhook-consumer`, `cmd/file-consumer`
   - `rest`: `go list ./...` minus all of the above (74 of 80 packages
     today), so a new package is tested without being listed.
   A named package that disappears makes `go list` fail, which fails the
   leg instead of testing less.
3. The integration-tag `pkg/io` state-store test runs only in the leg that
   holds `pkg/io` (`rest`).
4. `go-lint` ("Go lint"): promtool, gofmt, golangci-lint, lint-openapi,
   lint-tenant, in parallel with the shards. Steps unchanged.
5. `go-tests` ("Go Tests"): `needs: [go-test-shards, go-lint]`,
   `if: always() && <same event condition>`, green only when both results
   are `success`.
6. `plan` needs `go-tests`, `go-test-shards` and `go-lint`. Its existing
   `!contains(needs.*.result, 'failure'|'cancelled')` gate then stops a
   deploy on any red shard; `build-images` and `deploy` need `plan`, so the
   deploy still waits for every shard.

Coverage: `-cover` only prints a per-package percentage in the log; nothing
collects a profile (no `-coverprofile`, no upload). Each package still runs
whole in one shard, so the printed numbers are unchanged.

## Guard tests

- New `TestGoTestShardsCoverEveryPackage`
  (`src/pkg/managementapi/gotestshards_guard_test.go`): the matrix and the
  script name the same shards; their packages together equal
  `go list ./...`, each exactly once; the shard step uses the script with
  `matrix.shard`, `-race`, the Postgres URL and the skip check; the job has
  the postgres service; the `pkg/io` integration step is pinned to the shard
  that owns `pkg/io`; "Go Tests" needs both jobs with `always()`; `plan`
  needs all three. Mutation run: 11 mutations (drop a shard from the matrix
  or the script, drop `-race`, drop the skip check, drop a package from
  `rest`, put a package in two shards, move or unpin the `pkg/io` step,
  drop a need) all fail it.
- `TestGoJobsRetryModuleDownloads`: logic unchanged and still passes (it
  now counts the shard job and Go lint, 7 jobs, above its floor of 6); only
  the failure message's list of known jobs is updated.
- `TestDeployJobGatesOnTests`: unchanged; it finds `go-test-shards` as a job
  that runs `go test` and checks `plan` needs it.

## Affected files

- `.github/workflows/build-push.yml`
- `.github/scripts/go-test-shards.sh` (new)
- `src/pkg/managementapi/gotestshards_guard_test.go` (new)
- `src/pkg/managementapi/nodeconfig_test.go` (one message)
- `docs/DEPLOYMENT_GUIDE.md` (CI job table)

## Risks

- Four Go runners plus a lint runner instead of one: more runner minutes
  (setup and compile repeat per leg). The repo is on GitHub-hosted runners;
  the trade is minutes for wall time.
- All legs share the setup-go cache key; only the first to finish saves it.
  Harmless, the others log that the key exists.
- `.github/workflows/` changes: `agent:needs-human`, no merge by an agent.

## Verification

- `go test -count=1 ./pkg/managementapi/...`, `go vet ./...`, gofmt.
- actionlint is not installed locally; CI parsing the workflow on the PR is
  the check.
- Before/after wall times from real runs, in the PR.
