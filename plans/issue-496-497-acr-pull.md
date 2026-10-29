# #496 + #497 The acr-pull copies: refresh vrsky-ui, never print the Secret

Backlog items 93 and 94, both found in #431.

- #497: `vrsky-ui` gets its `acr-pull` copy only from `deploy-ui-azure.sh`,
  which a human runs. The deploy job rolls the UI with
  `deploy-core-azure.sh`, which doesn't copy, so after an ACR password
  rotation the next UI rollout can't pull.
- #496: `deploy-backup-azure.sh` pipes the copy into a bare
  `kubectl apply -f -`. A failed apply on an existing Secret can print
  "error when applying patch:" with the new `.dockerconfigjson` into the
  deploy log, which anyone with repo access can read. `deploy-ui-azure.sh`
  has the same leak (and its terminal output gets pasted into issues).

## Open questions

None.

## Approach

1. New `infrastructure/azure/acr-pull-lib.sh`, sourced, not run. One
   function, `apply_acr_pull <secret-json> <namespace>`: the jq filter
   from `copy-acr-pull.sh` (type and data only, metadata reduced to name
   and namespace) piped into `kubectl apply -f -`, with stdout and stderr
   captured. On success it prints kubectl's line; on failure only
   "ERROR: apply of <ns>/acr-pull failed (exit N); output withheld …" and
   returns N. bash, jq and kubectl only, because two of the three callers
   run in-cluster. It lives in `infrastructure/azure/`, which both
   workflows stage whole, so `in-cluster.sh` uploads it.
2. `copy-acr-pull.sh`: one loop over `vrsky-database vrsky-ui`. Source
   lookup as today (missing source: exit 0). Per namespace a plain
   assignment for the lookup, so a Forbidden or timeout still stops the
   script under `set -e`; a missing namespace prints why and moves to the
   next one. The first failed apply stops the script (fail fast, like
   every other step here).
3. `deploy-backup-azure.sh`: section 1 keeps its lookup (it must fail when
   the source is missing) and calls `apply_acr_pull`. Sections 2 and 3,
   the `suspend` decision and the CronJob apply, are untouched.
4. `deploy-ui-azure.sh`: drops the `get -o yaml | sed | apply` copy for a
   `get -o json` lookup and `apply_acr_pull`. The copy now carries no
   labels or annotations from the source, as the other two already do.
   The laptop needs jq for this script now; noted in its header and in
   `docs/DEPLOYMENT_GUIDE.md`.
5. `TestInClusterScriptsUseOnlyRuntimeTools` follows `source`/`.` of a
   sibling script, so the helper is checked for sed and friends too
   (otherwise nothing in the workflows names it).

### After review (round 1)

The first version left the workflows alone, and that was wrong. The
deploy job ran the refresh after "Roll core services" and the smoke check,
and Catch up never ran `copy-acr-pull.sh`. Here is what happens after an
ACR rotation. The next merge that touches the UI rolls UI pods with the
stale `vrsky-ui/acr-pull`. They sit in ImagePullBackOff, the rollout times
out, and the job goes red before the refresh runs. A Catch up after a
rotation made while the cluster was parked fails the same way. So:

6. `build-push.yml` deploy job: the refresh step moves up, between "Stage
   the deploy files" and "Roll core services by digest". It is still gated
   only on the cluster running, and is renamed "Refresh the acr-pull copies
   in vrsky-database and vrsky-ui". The trade: a failed copy now fails the
   run before the rollout and the smoke check. That is the right order,
   because a stale copy would fail the rollout anyway.
7. `catch-up.yml`: the same step, unconditional, after staging and before
   "Roll every core service".
8. `TestACRPullCopyRefreshesOnEveryDeploy` pins the step in both workflows
   to after staging and before the first rollout step (`deploy-core-azure.sh`,
   `deploy-connectors-azure.sh` or `rollout restart`). It also pins its `if`.
9. A test sources the helper into a shell without pipefail and feeds it
   JSON that jq can't parse. It expects the generic line, a non-zero rc and
   no pipefail leaking into the caller.
10. All four files get a "never run with -x" header line.
11. The guard's `sourced` regex also matches `. lib.sh` with no slash, and a
    trailing `;`, `&`, `#` or whitespace. Its own table test covers these
    cases.

The workflow edits touch only the deploy job's steps and `catch-up.yml`.

## Touches

- `.github/workflows/build-push.yml` (deploy job: move and rename one step)
- `.github/workflows/catch-up.yml` (one step)
- `infrastructure/azure/acr-pull-lib.sh` (new)
- `infrastructure/azure/copy-acr-pull.sh`
- `infrastructure/azure/deploy-backup-azure.sh` (section 1 only)
- `infrastructure/azure/deploy-ui-azure.sh` (step 1 only)
- `src/pkg/managementapi/nodeconfig_test.go` (stub kubectl and guard tests)
- `docs/operator/deploy.md`, `docs/operator/backup.md`,
  `docs/DEPLOYMENT_GUIDE.md`, `infrastructure/kubernetes/backup/README.md`,
  `AGENTS.md`
- `plans/backlog.md` (strike 93 and 94)

## Acceptance criteria

1. `copy-acr-pull.sh` applies a clean copy into `vrsky-database` and
   `vrsky-ui`. A missing namespace skips only that one; no source Secret is
   a no-op; a failed source or namespace lookup exits non-zero.
2. A failed acr-pull apply in each of `copy-acr-pull.sh`,
   `deploy-backup-azure.sh` and `deploy-ui-azure.sh` exits non-zero,
   prints the generic line, and never prints the Secret's data or
   anything kubectl said.
3. `deploy-ui-azure.sh` still copies into `vrsky-ui` without the source's
   identity.
4. `deploy-backup-azure.sh`'s `suspend` behaviour is unchanged
   (`TestDeployBackupScript` unchanged and green).
5. The helper passes `TestInClusterScriptsUseOnlyRuntimeTools`, and that
   test would catch a sed in it.

## Risks

- The first deploy after merge re-applies `vrsky-ui/acr-pull` from the jq
  copy. Same type and data, minus labels and annotations the sed copy
  carried over; `kubectl apply` drops them. Pods already running keep
  their images; the next pull uses the same credentials.
- A failed apply now hides kubectl's reason. The exit code stays, and the
  operator can re-run the apply by hand to see it. That trade is the
  point of #496.
- `infrastructure/azure/**`: PR plus `agent:needs-human`.

## Verification

- `bash -n` on the four scripts.
- From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -count=1 ./pkg/managementapi/`.
- Mutations, each must fail a test: drop `vrsky-ui` from the loop; `exit 0`
  instead of `continue` on a missing namespace; a lookup inside
  `[ -z "$(…)" ]`; a bare apply in each of the three scripts; a `sed` in
  the helper.
- `affected.sh` on the diff, to say what merging deploys.
