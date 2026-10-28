# #395 Layer 2: encrypted logical dumps of the management Postgres

Layer 2 of [`plans/prod-backup.md`](https://github.com/ValueRetail/vrsky/blob/plans/prod-backup/plans/prod-backup.md)
(PR #396). Agent part only. The PR touches workflows, `infrastructure/azure`
and manifests, so it gets `agent:needs-human` and a human merges it.

## Open questions

None. The three in `plans/prod-backup.md` were answered on 2026-10-28
(Ludvik left the choice to the coordinator) and the docs use them:

- Layer 1: daily disk backup, kept 7 days, no weekly copies.
- Layer 2: a lifecycle rule deletes dumps older than 14 days.
- No second region for now; noted as a later option.
- `ENCRYPTION_KEY` is also kept outside Azure, in a password manager. The
  dumps are useless without it.

## Acceptance criteria

1. `vrsky-cli` is built into ACR as `vrskyprodacr.azurecr.io/vrsky/vrsky-cli`:
   - `build-push-acr.sh` has a `tools` group (`build_tools`), included in `all`.
   - `affected.sh` prints `image vrsky-cli` for a change under
     `src/cmd/vrsky-cli/` and for every shared-Go change (`src/pkg`,
     `go.mod`/`go.sum`, any `Dockerfile` under `src/cmd`). Its `TOOLS` list
     equals what `build_tools` builds.
   - The `build-push.yml` matrix picks it up from the plan job with no
     special case (the generic `src/cmd/<img>/Dockerfile` branch builds it).
2. `infrastructure/kubernetes/backup/cronjob.yaml`:
   - image `vrskyprodacr.azurecr.io/vrsky/vrsky-cli:latest`, `imagePullPolicy: Always`
   - `schedule: "0 12 * * 1-5"`, `timeZone: Europe/Oslo`
   - `startingDeadlineSeconds: 3600`, `concurrencyPolicy: Forbid`
   - `suspend: true` in the file (the deploy step decides the live value)
   - `imagePullSecrets: [acr-pull]`
   - pod: `runAsNonRoot`, uid/gid 70 (the `postgres` user of the alpine
     image), seccomp `RuntimeDefault`, no service-account token
   - container: `readOnlyRootFilesystem`, no privilege escalation, all
     capabilities dropped, an `emptyDir` at `/tmp`
   - every setting comes from `envFrom: vrsky-backup-config`; no `value:`
     carries a credential
   - CPU request 100m; `TestCPURequestsStayWithinBudget` counts it and
     still passes.
3. `infrastructure/azure/deploy-backup-azure.sh` (run in-cluster):
   - copies `acr-pull` from `vrsky-platform` into `vrsky-database` with
     `kubectl` + `jq` (no `sed`, the runtime has none)
   - never creates, applies, patches or prints `vrsky-backup-config`; it
     only reads the key names
   - applies the CronJob, then sets `spec.suspend`: `false` when the Secret
     exists with `BACKUP_DB_URL`, `ENCRYPTION_KEY`, `BACKUP_PROVIDER` and
     `BACKUP_BUCKET`; `true` otherwise, with a log line naming what is
     missing. **Decision: apply suspended, not skip.** The CronJob, the
     image pull and the alert wiring are then in place before the human
     step, and nothing runs (or fails) until the Secret is there.
   - exits non-zero only on a real error (no `acr-pull` to copy, apply
     failed), not on a missing Secret.
4. Deploy wiring:
   - `affected.sh` prints `backup` for `infrastructure/kubernetes/backup/*`
     and `infrastructure/azure/deploy-backup-azure.sh`.
   - `build-push.yml`: plan output `backup`; the stage step copies
     `infrastructure/kubernetes/backup`; a deploy step runs the script
     through `in-cluster.sh`, gated on the cluster running.
   - `catch-up.yml` runs it too, so a merge while parked is applied on the
     next Catch up, and the suspend flag is re-evaluated after the human
     creates the Secret.
5. Alert `MgmtBackupMissing` in a new rule group `backup`, severity
   `warning`, from kube-state-metrics, scoped to
   `namespace="vrsky-database"`, `cronjob="vrsky-mgmt-backup"`. Fires when
   the CronJob is not suspended and either:
   - (a) two or more of its Jobs have failed since the last successful one
     (or since the CronJob was created, if none ever succeeded). A Job is
     created at most once per running weekday, so this is "two running
     weekdays without a backup", and it doesn't count days the cluster was
     parked. Last success comes from `kube_job_status_completion_time`
     (set only on success) joined to `kube_job_owner`.
   - (b) the CronJob has not been scheduled for more than 98 hours,
     evaluated only Monday to Friday after 12:00 UTC (so after the noon
     Oslo run). Catches a CronJob that stopped firing at all. 98h is
     Thursday's run to Monday afternoon, two missed weekdays across a
     weekend.
   - Suspended: silent, whatever else holds.
   - Group `backup`, not `vrsky-*`: `smoke-alerts.sh` fails a deploy on any
     firing `vrsky-*` alert above `info`. A failing backup is not caused by
     a code deploy and can't be fixed by one; it reaches Teams through
     Alertmanager like any other platform alert.
   - promtool tests: fires after two failures, quiet after one, quiet when
     the next run succeeds, quiet when suspended, (b) fires when nothing is
     scheduled, (b) quiet before 12:00 UTC.
6. `dr-drill.yml`:
   - source and target are `postgres:18-alpine`, database and user `vrsky`
     like AKS
   - every `infrastructure/migrations/*.up.sql` applied in order, then rows
     seeded for two tenants (users, tenants, connections, secrets)
   - backup and restore run the real image built from
     `src/cmd/vrsky-cli/Dockerfile`, as uid 70 with a read-only root
     filesystem and a tmpfs at `/tmp` (the CronJob's settings)
   - storage is Azurite with `BACKUP_PROVIDER=azure` and
     `BACKUP_BUCKET=vrsky-backups` (prod's provider and container name)
   - after restore, per-tenant row counts on the target equal the seeded
     numbers and the source's
   - weekly `schedule:` Wednesday 06:00 UTC, plus the existing path
     triggers and `workflow_dispatch`
7. Guard tests in `src/pkg/managementapi/nodeconfig_test.go` for 1–4 (see
   below), written by a separate test author from these criteria.
8. Docs: `docs/DR.md`, `docs/operator/backup.md` and the backup README
   describe what runs, the human setup (storage account, container, the
   `kubectl create secret` command with placeholders, unsuspending) and
   restore runbooks for a disk snapshot and a dump. Nothing claims a
   production run.

## Files

- `infrastructure/azure/build-push-acr.sh`, `affected.sh`, new `deploy-backup-azure.sh`
- `.github/workflows/build-push.yml`, `catch-up.yml`, `dr-drill.yml`
- `infrastructure/kubernetes/backup/cronjob.yaml`, `README.md`
- `infrastructure/prometheus-rules.yml`, `prometheus-rules-test.yml`
- `src/pkg/managementapi/nodeconfig_test.go`
- `docs/DR.md`, `docs/operator/backup.md`
- `plans/backlog.md` (one item noticed on the way)

## Decisions

- `:latest` with `imagePullPolicy: Always`, not a digest. Each Job pulls
  what ACR has; a rebuilt CLI takes effect at the next run without
  re-applying the CronJob. Same pattern as the connectors. The drift check
  only looks at `vrsky-platform` and `vrsky-ui`, so a backup pod never
  triggers Catch up.
- Apply suspended rather than skip when the Secret is missing (above).
- A deploy re-derives `suspend` from the Secret. A hand-suspended CronJob is
  resumed by the next deploy that touches backup or by Catch up; the docs
  say so.
- `acr-pull` is copied on every run, so a rotated ACR password follows.
- The alert is warning in a non-`vrsky-*` group (above).
- The drill uses Azurite's connection string. The account-name-plus-key
  path builds a `*.blob.core.windows.net` URL, which a CI emulator can't
  serve; both go through the same Azure store code after the client is
  built.

## Risks

- PromQL on kube-state-metrics series can't be tried against prod here.
  The promtool tests pin the expression to the metric names and labels
  KSM v2 exports; if the chart's KSM drops a label, the alert stays silent.
  The human check after setup: `kubectl create job --from=cronjob/...`
  with a broken Secret twice, and see the alert.
- uid 70 and a read-only root filesystem for `pg_dump`: proven in the
  drill, which runs the image with the same settings.
- The first deploy of this PR applies the CronJob suspended; nothing runs
  until the human creates the Secret and unsuspends (or Catch up runs).

## How we verify

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race ./...`, `make lint-tenant`
- `promtool check rules` and `promtool test rules` (the docker command from
  `build-push.yml`)
- `bash -n` on every changed script; the deploy script driven against a
  stub `kubectl` in the guard test
- the drill's seed SQL run locally against the migrations
- `docker build -f src/cmd/vrsky-cli/Dockerfile .` if the base images are
  cached; otherwise CI's DR drill builds it
- `mkdocs build --strict`
- mutations on the key guards and the alert
