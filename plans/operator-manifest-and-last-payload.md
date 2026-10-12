# Operator email in the manifest, and the last four `last_payload` leftovers

Both halves re-verified in the code on 2026-10-08 (`main` at `a01ead4`).

## Open questions

1. **Where the operator list lives.** Recommended: **the value itself in
   `deployment.yaml`** (`PLATFORM_OPERATORS: "ludvik.hoff@valueretail.no"`).
   It is an identity, not a secret, the manifest already carries the other
   non-secret config, and one place means no drift. Alternative: a
   `ConfigMap` created once by hand and referenced with `configMapKeyRef`
   (`optional: true`), which keeps the manifest generic but adds a second
   thing to remember when the cluster is rebuilt.
2. **A guard test** that the manifest names at least one operator
   (`TestManifestNamesAPlatformOperator`, a source test beside the other
   manifest guards in `nodeconfig_test.go`). Recommended: **yes** — the
   whole point is that nobody blanks it again. Alternative: rely on the
   docs.

## Goal

- A `kubectl apply` of the management-api manifest — which
  `infrastructure/azure/deploy-azure.sh` does on a rebuild, and anyone may
  do by hand — no longer removes the platform operator. Today the manifest
  says `PLATFORM_OPERATORS: ""`; the live value was set with `kubectl set
  env` on 2026-10-07 and would vanish on the next apply, after which nobody
  can activate a plan.
- The last four consumers that still write `last_payload` themselves, by
  connection id only, stop doing so. The SDK's publish closure already
  writes it for every consumer, tenant-scoped and throttled (one write per
  connection per 30 s); the webhook consumer's copy went in #310.

## What exists (checked)

- `infrastructure/kubernetes/management-api/deployment.yaml:83`:
  `PLATFORM_OPERATORS` with `value: ""` and a comment saying to set it with
  `kubectl set env`. Live: `PLATFORM_OPERATORS=ludvik.hoff@valueretail.no`.
  `deploy-core-azure.sh` sets images only; `deploy-azure.sh` applies a
  working copy of the manifests (it rewrites replicas and the service
  account, nothing else).
- `docs/operator/billing.md:45-56` documents the `kubectl set env` step.
- Leftover writes, each right after `s.publish(ctx, env)` — the SDK closure
  that already does `UPDATE … WHERE id = $2 AND tenant_id = $3` with the
  envelope's own tenant and connection:
  - `cmd/api-consumer/api_poller.go:321-325` (`json.Marshal(env)` + `Exec`)
  - `cmd/db-consumer/service.go:425-439` (marshals early "for the cache")
  - `cmd/file-consumer/server.go:210-217` (`if s.db != nil` block)
  - `cmd/tenant-consumer/bridge.go:167-185` (writes the *target*
    connection; `newEnv` carries the target tenant and connection, so the
    SDK write covers exactly that row)
- Their tests expect the write by regex without args and never call
  `ExpectationsWereMet` (`db-consumer/consumer_test.go:38`,
  `api-consumer/consumer_test.go:72`, `file-consumer/consumer_test.go:47,125`,
  `file-consumer/restore_test.go:43`); the SDK's write satisfies them, as it
  did for the webhook consumer.

## Approach

### 1. Manifest

`deployment.yaml`: `value: "ludvik.hoff@valueretail.no"`, comment rewritten
(what it is, that a rebuild applies it, how to add a second operator).
`docs/operator/billing.md`: the `kubectl set env` paragraph becomes "edit
the manifest and apply it; `kubectl set env` only until the next apply".

Guard: `TestManifestNamesAPlatformOperator` reads the manifest, finds the
`PLATFORM_OPERATORS` entry and fails when the value is empty or not a list
of `@`-addresses.

### 2. Consumers

Delete the four blocks (and the now-unused marshal in `db-consumer` and
`bridge.go`; `json` stays imported where still used). Comment in each:
"last_payload is written by the SDK publish closure, tenant-scoped,
throttled to one write per connection per 30 s."

Tests, same pattern as #310: the existing `last_payload` expectation gets
`WithArgs(sqlmock.AnyArg(), connID, tenant)` and the test ends with
`harness.Eventually(… mock.ExpectationsWereMet() == nil)`, so the SDK's
scoped write is what the test now proves, once per publish. The bridge
test (`tenant-consumer`) gets the same expectation on the target
connection if its sqlmock covers the bridge path; otherwise a note why not.

Behaviour change to state in the PR: for these four sources the preview
row can be up to 30 s behind the last message, as it already is for every
SDK-only consumer and, since #310, the webhook.

## Files

| File | Change |
|---|---|
| `infrastructure/kubernetes/management-api/deployment.yaml` | real operator value + comment |
| `docs/operator/billing.md` | manifest, not `kubectl set env` |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestManifestNamesAPlatformOperator` |
| `src/cmd/api-consumer/api_poller.go`, `src/cmd/db-consumer/service.go`, `src/cmd/file-consumer/server.go`, `src/cmd/tenant-consumer/bridge.go` | remove the write |
| `src/cmd/{api,db,file}-consumer/consumer_test.go`, `src/cmd/file-consumer/restore_test.go`, tenant-consumer bridge test | `WithArgs` + `ExpectationsWereMet` |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestManifestNamesAPlatformOperator` | the manifest names an operator | set the value back to `""` |
| api / db / file consumer tests with `WithArgs(…, connID, tenant)` + `ExpectationsWereMet` | the one remaining write is the SDK's, tenant-scoped | drop `AND tenant_id = $3` in `pkg/sdk/lifecycle.go` (already proven by the webhook tests; now four more) |
| existing suites | nothing else depended on the direct write | — |

Then `gofmt`, `go vet`, `golangci-lint`, `make lint-tenant`, `go test -race
./...`.

## Verify in prod (on your word)

1. `kubectl diff -f infrastructure/kubernetes/management-api/deployment.yaml -n vrsky-platform`
   no longer shows a `PLATFORM_OPERATORS` difference (image and replicas
   still differ, as always). No apply needed: live already equals the file.
2. After the four consumer rollouts: each restores its pipelines (none are
   running in prod today besides BC, webhook and remote-agent, so this is a
   log check), and the BC pipeline's preview in the builder still shows
   data.

## Rollout

`az acr build` for api-consumer, db-consumer, file-consumer,
tenant-consumer; `kubectl rollout restart` the four deployments. The
management API is not rebuilt (test file only) and not re-applied.

## Risks

- The email in a manifest in the repo: it is in every commit's author line
  already. If the repo ever goes public, the ConfigMap alternative is a
  five-line change.
- `deploy-azure.sh` on a rebuild now sets the operator correctly instead of
  to nobody — the fix, not a risk.

## Non-goals

A `platform_operators` table with a UI (the right thing once there is more
than one operator); the throttle interval; the preview endpoints themselves.
