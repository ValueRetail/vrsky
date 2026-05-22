# Encryption-at-rest overlay

Phase 1F / issue #71.

This directory contains manifests that promote a vanilla VRSky install
into **compliance mode**: every persisted byte lands on an encrypted disk,
and MinIO additionally wraps each object with a KMS-backed data key.

The base manifests in `../minio/`, `../postgresql/`, `../platform-nats/`
default to `storageClassName: local-path` which is **not** encrypted —
those are fine for development on k3d but inadequate for production.

## Contents

| File | What it does |
|------|--------------|
| `kes-deployment.yaml` | MinIO Key Encryption Service (KES) sidecar — mediates between MinIO and the actual KMS. File-backed key store by default; swap for Vault / AWS KMS / GCP KMS / Azure Key Vault in production. |
| `minio-encrypted.patch.yaml` | Kustomize patch that turns on `MINIO_KMS_AUTO_ENCRYPTION` and points MinIO at the KES service. |
| `storage-classes/aws-ebs-kms.yaml` | AWS gp3 encrypted with a customer KMS key. |
| `storage-classes/gcp-pd-cmek.yaml` | GCP Persistent Disk with customer-managed encryption keys. |
| `storage-classes/azure-cmk.yaml` | Azure Disk with customer-managed key. |
| `storage-classes/self-hosted-luks.yaml` | Notes on running LUKS under `local-path` on bare-metal / homelab. |

## Apply

For cloud:

```bash
# 1. Pick an encrypted StorageClass and apply it cluster-wide.
kubectl apply -f storage-classes/aws-ebs-kms.yaml

# 2. Re-create the PVCs of the base manifests so they bind to the new
#    StorageClass. (PVCs are immutable wrt storageClassName — this is a
#    destructive step in a fresh cluster only.)
kubectl -n vrsky-storage delete pvc minio-data
kubectl apply -k ../  # re-apply base + the patch below

# 3. Deploy KES + apply the MinIO encryption patch.
kubectl apply -f kes-deployment.yaml
kubectl apply -f minio-encrypted.patch.yaml
```

For self-hosted (LUKS):
- Follow `storage-classes/self-hosted-luks.yaml` to format the data
  partition before `local-path` claims any volumes on it.
- Skip the StorageClass swap; deploy KES + the MinIO patch as above.

## Health checks

Once `kes` and the patched `minio` are running:

```bash
# KES is reachable
kubectl -n vrsky-storage exec deploy/minio -- \
  curl -sk https://kes.vrsky-storage.svc.cluster.local:7373/version

# MinIO refuses uploads if KES is down (compliance mode)
kubectl -n vrsky-storage scale deploy/kes --replicas=0
mc cp /tmp/x s3/test/x   # expect KMS-related error
kubectl -n vrsky-storage scale deploy/kes --replicas=1

# Verify SSE on a new object
mc cat --metadata s3/test/x | grep -i sse
```

## What this does NOT cover

- **Backups**: the off-cluster backup bucket needs its own SSE config.
  Cross-cluster replication of KES keys is out of scope of this overlay.
- **Postgres column encryption**: handled separately by the secrets
  table (#66). The disk under Postgres must be encrypted by the
  StorageClass; per-column secrecy is the application's job.
- **TLS in transit**: covered by the ingress (Traefik + cert-manager) and
  by NATS' built-in TLS. Documented in `docs/SECURITY.md`.
