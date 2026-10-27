# Issue #386: stale backup and Traefik claims in the docs

Backlog item 28. No open questions; decisions are listed at the end.

## Goal

The docs say the management Postgres "is backed up daily" and that TLS
terminates at Traefik. Neither matches the repo. Correct both everywhere they
appear in `docs/` and the READMEs, say plainly what the repo can't tell about
production, and touch no manifests, workflows or scripts.

## Claims, evidence, new wording

### 1. "backed up daily" / "RPO ≤ 24h via the daily CronJob"

Where: `docs/DEPLOYMENT_GUIDE.md` "Disaster Recovery", `docs/DR.md` intro and
RPO/RTO table, `docs/operator/backup.md` "Tooling",
`infrastructure/kubernetes/backup/README.md` intro and setup step 1.

What the repo shows:

| Fact | File:line |
|------|-----------|
| CronJob `vrsky-mgmt-backup`, 02:00 UTC daily, namespace `vrsky-database` | `infrastructure/kubernetes/backup/cronjob.yaml:14-19` |
| Its image is `ghcr.io/valueretail/vrsky/vrsky-cli:latest` | `infrastructure/kubernetes/backup/cronjob.yaml:35` |
| It reads all config from Secret `vrsky-backup-config` | `infrastructure/kubernetes/backup/cronjob.yaml:37-39` |
| The backup README says CI builds the image | `infrastructure/kubernetes/backup/README.md:14` |
| No workflow or script builds a `vrsky-cli` image: ACR builds list management-api, ui, data-filter, data-converter, alert-responder, connectors | `infrastructure/azure/build-push-acr.sh:46-78`; `git grep vrsky-cli .github infrastructure/azure` finds only `dr-drill.yml` |
| A Dockerfile for the image exists, used only by the compose `backup` profile | `src/cmd/vrsky-cli/Dockerfile`, `docker-compose.yml:1183-1188` |
| Nothing applies the CronJob: no script under `infrastructure/azure/` or `deploy-vrsky-platform.sh` mentions `backup` | `git grep -n -i backup -- infrastructure/azure infrastructure/kubernetes/deploy-vrsky-platform.sh` (empty) |
| AKS can't pull `ghcr.io` images | `docs/DEPLOYMENT_GUIDE.md:283-285` |
| No script or workflow takes disk or volume snapshots; Postgres runs in-cluster on a `managed-csi` PVC | `infrastructure/azure/deploy-azure.sh:8,19,31`, `infrastructure/kubernetes/postgresql/statefulset.yaml:28`; `git grep -i snapshot -- infrastructure/azure .github` (empty) |
| Production Postgres is 18 | `infrastructure/kubernetes/postgresql/statefulset.yaml:28` |
| Production database and user are both `vrsky`, not `management_db` | `infrastructure/kubernetes/postgresql/configmap.yaml:7-8`, `infrastructure/azure/rotate-secrets.sh:25-34` |

The repo can't tell whether someone applied the CronJob by hand, or set up
Azure-side backups of the disk. If the CronJob was applied unchanged, it
can't pull its image.

New wording: the tooling exists and the restore path is tested in CI; nothing
in the repo builds the image, applies the CronJob or snapshots the volume, so
as far as the repo shows no scheduled backup runs in production. RPO ≤ 24h and
RTO ≤ 1h become targets, not met today. Point at backlog item 27.

### 2. The DR drill

Where: `docs/DR.md` "The CI restore drill", `docs/operator/backup.md`
"Restore drill", the guide's "restore path is tested in CI".

What `.github/workflows/dr-drill.yml` does:

| Fact | Line |
|------|------|
| Runs on push and PR to main/develop when `src/cmd/vrsky-cli`, `src/pkg/crypto`, `src/pkg/objectstore`, `infrastructure/migrations` or the workflow change, and by hand; no schedule | 8-25 |
| Source and target are `postgres:16-alpine` service containers | 48-49, 59-60 |
| Builds the binary with `go build`, not the Docker image | 86 |
| MinIO in the runner | 94-102 |
| Seeds one table with two rows, no migrations | 109-112 |
| backup, list, restore, assert 2 rows | 116-130 |

New wording: says exactly that, and what it doesn't cover (the image, the
CronJob, the Secret, the real schema, Postgres 18, any production storage,
whether a backup exists).

### 3. `docs/operator/backup.md` command block

The flags `--out`, `--bucket` and `--in` don't exist. `backup` takes only
`--stamp` (`src/cmd/vrsky-cli/backup.go:21`) and reads its target from
`BACKUP_*` env; `restore` takes `--target-db-url`, `--confirm`,
`--allow-source-db` and a key (`src/cmd/vrsky-cli/restore.go:24-35`). Replace
the block with the real commands, as `docs/DR.md` already shows them.

### 4. "TLS terminated at Traefik, internal mTLS via the cluster mesh"

Where: `docs/DEPLOYMENT_GUIDE.md:440`,
`infrastructure/kubernetes/encryption/README.md:73`.

What the repo shows:

| Fact | File:line |
|------|-----------|
| The production Ingress `vrsky` uses class `nginx`, ClusterIssuer `letsencrypt-prod`, TLS secret `vrsky-tls` | `infrastructure/kubernetes/ui/ingress.yaml:29,39,45-50` |
| The webhook and agent Ingresses are class `nginx` and reuse that certificate | `infrastructure/kubernetes/ingress/webhooks-ingress.yaml:22-24,47`, `agent-ingress.yaml:33,55` |
| ClusterIssuers `letsencrypt-staging` and `letsencrypt-prod`, HTTP-01 through class `nginx` | `infrastructure/kubernetes/ingress/cert-issuers.yaml:4,21,26,43` |
| Neither ingress-nginx nor cert-manager is installed by the Azure scripts | `docs/DEPLOYMENT_GUIDE.md:262-267` |
| No service mesh or cluster CA anywhere in the repo | `git grep -i -E "linkerd|istio|cluster-trust|trust-manager"` finds only the guide's own row |
| Behind the ingress, traffic is plain: UI nginx proxies to `http://` management-api, Postgres URLs use `sslmode=disable`, NATS manifests have no TLS | `infrastructure/azure/deploy-ui-azure.sh:17,37,44`, `infrastructure/azure/rotate-secrets.sh:34`, `git grep -i tls -- infrastructure/kubernetes/platform-nats infrastructure/kubernetes/tenant-nats` (empty) |

New wording: TLS terminates at ingress-nginx with a Let's Encrypt certificate
from cert-manager; inside the cluster, traffic is unencrypted per the
manifests.

### 5. Traefik as the production gateway

Where: `docs/security/whitepaper.md:55`, `docs/SECURITY.md:352-353`.

What the repo shows: the management API writes Traefik rate-limit config only
when `TRAEFIK_DYNAMIC_DIR` is set (`src/cmd/management-api/main.go:451-454`).
Only `docker-compose.yml:395` sets it; the Kubernetes management-api manifests
don't, and no script applies `infrastructure/kubernetes/traefik/`. Production
ingress is ingress-nginx (item 4).

New wording: the per-plan gateway rate limit runs in the compose stack; the
Kubernetes manifests exist but aren't wired up, and production has no Traefik.

## Left alone

- `docs/operator/install.md:23`: compose, where Traefik does run.
- `infrastructure/kubernetes/ingress/README.md` Traefik lines: K3s's default
  controller, accurate for K3s.
- `README.md:104`: roadmap row, "under research".
- `infrastructure/kubernetes/backup/cronjob.yaml` header comment: a manifest
  (backlog item 27).
- `infrastructure/kubernetes/postgresql/README.md:265`: an instruction to
  create a CronJob, not a claim one runs.
- `infrastructure/kubernetes/management-api/README.md:14,48`: Traefik as the
  gateway and the SSE sticky-session advice. The advice needs rethinking
  (the UI's nginx proxies `/api`, so ingress affinity doesn't reach the
  replicas); new backlog item.
- `B2B_READINESS_PLAN.md`, `plans/`: plans, not docs.
- The other rows of the encryption-at-rest table (encrypted StorageClass,
  KES): not part of this issue, and nothing on AKS applies them either
  (`git grep -i -E "kes|vrsky-encrypted|encryption/" -- infrastructure/azure
  .github/workflows` finds nothing relevant). New backlog item 37.

## Files

- `docs/DEPLOYMENT_GUIDE.md`
- `docs/DR.md`
- `docs/operator/backup.md`
- `docs/SECURITY.md`
- `docs/security/whitepaper.md`
- `infrastructure/kubernetes/backup/README.md`
- `infrastructure/kubernetes/encryption/README.md`
- `plans/backlog.md` (strike 28, add items 35 to 37)
- this plan

Two READMEs sit under `infrastructure/kubernetes/`. They are docs, no
manifest changes.

## Verification

- `mkdocs build --strict` in a scratch venv with mkdocs-material, outside the
  repo.
- Re-run the greps above after the edit: no "backed up daily" or "Traefik"
  claim about production left in `docs/` or the READMEs.
- Every claim in the new text checked against the file:line list above.

## Decisions

- Kept RPO ≤ 24h and RTO ≤ 1h as targets rather than deleting them.
- Fixed the "internal mTLS via the cluster mesh" half of the TLS row too,
  since no mesh exists in the repo.
- Fixed the wrong `vrsky-cli` flags in `docs/operator/backup.md` because they
  sit in the paragraph being corrected.
- The production database name mismatch (`vrsky` vs `management_db` in the
  backup README's Secret example) goes to the backlog with item 27's work,
  not into this PR.
