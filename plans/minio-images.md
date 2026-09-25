# MinIO images gone from quay.io — replacement plan

## Open questions

1. **Where does prod pull from?**
   - (a) Straight from Docker Hub `pgsty/*`: simplest.
   - (b) A copy imported into `vrskyprodacr`: survives the next upstream deletion. Needs a pull secret or ACR attach for `vrsky-storage`, which is not verified.
   - Recommendation: **(b)**.
2. **Which PR?** A separate PR from `main`, not #273 (one PR per chunk). #273's uncommitted watcher-test fix stays on its own branch.

## What happened (verified 2026-09-24)

- `quay.io/minio/minio` and `quay.io/minio/mc` return **401** for every tag, including prod's pinned `RELEASE.2025-09-07T16-13-09Z`. Other quay images pull fine.
- Docker Hub `minio/*` was deleted on 2026-09-11, so quay was already our fallback (`docs/operator/troubleshooting.md:35`).
- `quay.io/minio/kes` still pulls today. It is at risk, but out of scope.
- The Mac's cached copies are **arm64 only**. They are useless for the amd64 AKS nodes.
- `vrskyprodacr` holds no MinIO image.

## Prod risk (the urgent part)

- `vrsky-prod` is **Stopped**, and its node pool uses **Ephemeral OS disks**.
- Stopping deallocates the nodes, so the image cache is gone on start. The prod MinIO pod **will** hit ImagePullBackOff. This is certain, not a maybe.
- Impact: claim-check for payloads over 256 KB fails, which covers large pipeline payloads and remote-agent deliveries over 256 KB. Small messages keep working.
- The data is on a `managed-csi` PVC and is safe. The bucket holds only `spill/` and `temp/` objects, which have a 1-day TTL.
- **Do not start the cluster until the prod image is repointed.** Otherwise, repoint right after starting it.

## Options compared

| Option | Pulls? | Pinned tags | Shell for our `mc` scripts | Verdict |
|---|---|---|---|---|
| **`pgsty/minio` + `pgsty/mc`** (community fork, Docker Hub) | ✅ amd64 + arm64 | ✅ `RELEASE.2026-08-04T00-00-00Z`, mc `RELEASE.2026-09-16T00-00-00Z` | ✅ `/bin/sh` | **Recommended** |
| Chainguard `cgr.dev/chainguard/minio`, `minio-client` | ✅ amd64 + arm64 | free tier `latest` only (pin by digest) | ❌ distroless, so `minio-init`, `setup-job` and the DR drill need rewriting | fallback |
| `bitnamilegacy/minio` | ✅ | ✅ | ✅ | frozen, never patched — no |
| Mirror the old image into ACR | ❌ the source is gone, and the local cache is arm64 | — | — | not possible |
| Another S3 server for CI only (SeaweedFS, Garage…) | — | — | — | CI tests use MinIO-specific `MINIO_KMS_SECRET_KEY` (SSE-S3) and `mc ilm`. It's churn, and prod would still need an image — no |

**pgsty smoke test, run locally with our exact compose settings:**
- The server starts with `MINIO_KMS_SECRET_KEY` and the console flag.
- The `mc` image has `/bin/sh`.
- `mc mb`, `mc ilm rule add --prefix spill/ --expire-days 1` and `mc admin info` all work.
- `mc cp --enc-s3` stores objects with `X-Amz-Server-Side-Encryption: AES256`.

**Trust:**
- It is maintained by Pigsty (PGSTY), 748k pulls, with regular releases (2026-06, 2026-08).
- It is one vendor's fork, which is why prod should run an ACR copy we control (question 1).

## Changes (repo)

| File | Change |
|---|---|
| `docker-compose.yml:1119,1136` | `pgsty/minio:RELEASE.2026-08-04T00-00-00Z`, `pgsty/mc:RELEASE.2026-09-16T00-00-00Z`. Pinned, replacing `latest`. |
| `.github/workflows/dr-drill.yml:90,95` | Same pins. |
| `infrastructure/kubernetes/minio/deployment.yaml:36`, `statefulset-distributed.yaml:96` | `pgsty/minio:RELEASE.2026-08-04T00-00-00Z` |
| `infrastructure/kubernetes/minio/setup-job.yaml:19` | `pgsty/mc:RELEASE.2026-09-16T00-00-00Z` |
| `infrastructure/kubernetes/minio/README.md` (4× `kubectl run … mc`) | `pgsty/mc:<tag>` |
| `docs/operator/troubleshooting.md:35` | Extend the existing note: quay went too on 2026-09-24; now on `pgsty/*`, with the ACR copy for prod. |

- Not touched: `minio-go` (a Go module, unaffected) and KES (it still pulls).
- The only upgrade is prod's server version, 2025-09-07 → fork 2026-08-04. It's a forward upgrade of the same codebase. There is no rollback image anyway.

## Prod steps (you run these)

```bash
# 1. Copy into ACR (works while the cluster is stopped)
az acr import -n vrskyprodacr --source docker.io/pgsty/minio:RELEASE.2026-08-04T00-00-00Z --image minio/minio:RELEASE.2026-08-04T00-00-00Z
az acr import -n vrskyprodacr --source docker.io/pgsty/mc:RELEASE.2026-09-16T00-00-00Z --image minio/mc:RELEASE.2026-09-16T00-00-00Z
# 2. Start the cluster, then repoint (plus a pull secret in vrsky-storage if ACR isn't attached)
kubectl -n vrsky-storage set image deploy/minio minio=vrskyprodacr.azurecr.io/minio/minio:RELEASE.2026-08-04T00-00-00Z
```

- Before step 2, I'll check how `vrsky-storage` authenticates to ACR: is ACR attached to AKS, or is there an `acr-pull` secret? If neither, add a secret plus `imagePullSecrets`.

## Risks

- **Fork supply chain:** one vendor. Mitigation: pinned tags in the repo, and prod runs an ACR copy.
- **Version jump in prod:** low risk, because the data is ephemeral (1-day TTL).
- **Docker Hub anonymous rate limit in CI:** the pull step already retries.

## How we verify

1. Local: `docker compose pull minio-test minio-init`, then `up`. Run the S3 connector integration tests as the workflow does.
2. PR CI: **Connector integration tests** and **DR drill** turn green.
3. Prod, after your steps: the MinIO pod runs with the new image, `mc admin info` works, and the spill bucket plus lifecycle rules are still there. A payload over 256 KB goes through a pipeline, re-running the claim-check check from 2026-08-26.

## Sources

- [MinIO removed from Docker Hub 2026-09-11 (flowershow#1382)](https://github.com/flowershow/flowershow/issues/1382)
- [grafana/mimir#16576: moved to quay after the Docker Hub removal](https://github.com/grafana/mimir/pull/16576)
- [pgsty/silo: MinIO fork maintained by PGSTY](https://github.com/pgsty/silo)
- [pgsty/minio tags](https://hub.docker.com/r/pgsty/minio/tags)
- [Chainguard free MinIO images](https://www.chainguard.dev/unchained/secure-and-free-minio-chainguard-containers)
- [Dokploy templates#1012: migrated to pgsty/minio](https://github.com/Dokploy/templates/pull/1012)
