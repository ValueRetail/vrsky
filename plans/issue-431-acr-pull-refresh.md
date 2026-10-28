# #431 Refresh the acr-pull copy in vrsky-database on every deploy

Backlog item 56. `deploy-backup-azure.sh` copies the `acr-pull` Secret from
`vrsky-platform` into `vrsky-database`, so the backup Job can pull
`vrsky-cli` from ACR. Only Catch up and a merge that touches the backup run
that script. After an ACR password rotation the copy keeps the old password
and the next backup Job fails with ImagePullBackOff until someone runs
Catch up.

## Open questions

None.

## Where acr-pull is written

- `infrastructure/azure/deploy-azure.sh` step 2 creates it in
  `vrsky-platform` and `vrsky-storage` from `az acr credential show`. A
  human runs that script from a laptop; it is the only place the password
  enters the cluster.
- `minio-images-acr.sh --repoint` creates it in `vrsky-storage` the same way.
- `deploy-ui-azure.sh` copies it into `vrsky-ui` (human-run only).
- `deploy-backup-azure.sh` copies it into `vrsky-database`.
- `rotate-secrets.sh` doesn't touch it.

There is no rotation runbook. A rotation can land in `vrsky-platform`
through `deploy-azure.sh` or a hand-typed `kubectl create secret`, so
hooking only `deploy-azure.sh` would miss the second. The copy has to
follow the source on its own.

## Approach

A cheap, idempotent copy step on every deploy, separate from the backup
script.

1. New `infrastructure/azure/copy-acr-pull.sh`: reads `acr-pull` from
   `vrsky-platform`, strips its identity (uid, resourceVersion, …) with jq,
   and applies it into `vrsky-database`. Only bash, jq and kubectl, like the
   other in-cluster scripts.
   - No source Secret (first install): prints why and exits 0.
   - No `vrsky-database` namespace: prints why and exits 0.
   - Any other kubectl error: exits non-zero. Each lookup is a plain
     assignment so `set -e` sees a failure; inside `[ -z "$(…)" ]` a
     Forbidden would pass for a missing object (review of #485).
   - The apply's output is captured. A failed apply on an existing object
     can print the patch, new `.dockerconfigjson` included, and the deploy
     log is public to anyone with repo access, so on a failure the script
     prints only a generic line and the exit code. The same leak in
     `deploy-backup-azure.sh` and `deploy-ui-azure.sh` is backlog item 93.
   - Touches nothing but that one Secret. It never reads or applies the
     CronJob, so it can't change `suspend`. That's the reason for not just
     running `deploy-backup-azure.sh` on every deploy: it re-decides
     `suspend` from the config Secret and would turn back on a CronJob a
     human suspended for a restore (docs/DR.md, #432).
2. `build-push.yml` deploy job: a step "Refresh the acr-pull copy in
   vrsky-database" after the smoke check and before the backup step, gated
   only on `steps.cluster.outputs.state == 'Running'`. One more
   `az aks command invoke`, around half a minute. `kubectl apply` of an
   unchanged Secret is "unchanged", so it doesn't bump the Secret.
3. Catch up needs no change: its backup step runs on every Catch up and
   already copies.
4. `deploy-backup-azure.sh` keeps its own copy. It must fail when the
   source is missing (the CronJob would never pull), where the new script
   must not, and leaving it alone keeps `TestDeployBackupScript` as is.
   The jq filter is the same in both.

`affected.sh` doesn't map the new script to a target. The step runs on
every deploy, so a change to it takes effect on the next deploy of
anything.

## Touches

- `infrastructure/azure/copy-acr-pull.sh` (new)
- `.github/workflows/build-push.yml` (deploy job, one step)
- `src/pkg/managementapi/nodeconfig_test.go` (guard tests)
- `docs/operator/backup.md`, `docs/operator/deploy.md`, `AGENTS.md`,
  `infrastructure/kubernetes/backup/README.md`
- `plans/backlog.md` (strike 56, add the `vrsky-ui` gap)

## Acceptance criteria

1. Every deploy job run with the cluster up runs `copy-acr-pull.sh` through
   `in-cluster.sh`, whatever the plan's targets, after the files are staged.
2. Every Catch up refreshes the copy (it runs `deploy-backup-azure.sh`
   unconditionally today; the test pins that).
3. The script copies type and data into `vrsky-database` without the
   source's metadata, exits 0 with no apply when the source or the target
   namespace is missing, and never touches a CronJob or `suspend`.
4. The script uses only runtime tools (`TestInClusterScriptsUseOnlyRuntimeTools`
   picks it up from the workflow).
5. Existing guards stay green: `TestDeployJobGatesOnTests`,
   `TestDeployJobSkipsWhenClusterStopped`, `TestEveryLintCheckStillGates`,
   `TestInClusterScriptsUseOnlyRuntimeTools`, `TestAffectedServices*`,
   the backup tests.
6. Docs say what refreshes the copy and when.

## Risks

- The step fails the deploy if the apply fails. That's a real error worth
  seeing, and it runs after the smoke check, so the deploy's own result is
  already reported.
- The workflow change needs a human merge (`agent:needs-human`).

## Verification

- `bash -n` on the new script.
- From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -count=1 ./pkg/managementapi/`.
- Mutation run: drop the workflow step, gate it on `env.BACKUP == 'true'`,
  make the script exit 1 on a missing source, have it apply to
  `vrsky-platform`, have it keep the source's metadata. Each must fail a test.

## Not in scope

`vrsky-ui` has the same gap: `deploy-ui-azure.sh` copies `acr-pull` once
and the deploy job rolls the UI with `deploy-core-azure.sh`, which doesn't
copy. Filed as a backlog item.
