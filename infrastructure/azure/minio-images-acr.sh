#!/usr/bin/env bash
# minio-images-acr.sh — keep prod MinIO on images we control.
#
# MinIO's own images are gone: Docker Hub minio/* was deleted on 2026-09-11 and
# quay.io/minio/{minio,mc} started returning 401 on 2026-09-24. The manifests
# now name the pinned community fork pgsty/minio + pgsty/mc; prod runs a COPY
# of those tags in ACR, so the next upstream deletion cannot stop a pod from
# starting. (AKS nodes use ephemeral OS disks: after `az aks start` every
# image is pulled again.)
#
# The tags are read from the manifests, so there is one place to bump them.
#
#   infrastructure/azure/minio-images-acr.sh            import into ACR (safe while the cluster is stopped)
#   infrastructure/azure/minio-images-acr.sh --repoint  also point the running MinIO at the ACR copy
set -euo pipefail

REG="${REG:-vrskyprodacr}"
ACR_LOGIN="${REG}.azurecr.io"
NS=vrsky-storage
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

minio_ref="$(grep -oE 'pgsty/minio:[^[:space:]]+' infrastructure/kubernetes/minio/deployment.yaml | head -1)"
mc_ref="$(grep -oE 'pgsty/mc:[^[:space:]]+' infrastructure/kubernetes/minio/setup-job.yaml | head -1)"
[ -n "$minio_ref" ] && [ -n "$mc_ref" ] || { echo "ERROR: pgsty image tags not found in the MinIO manifests" >&2; exit 1; }

# pgsty/minio:TAG -> <registry>/minio/minio:TAG
acr_ref() { echo "${ACR_LOGIN}/minio/${1#pgsty/}"; }

import_image() {
  local src="$1" dest="minio/${1#pgsty/}"
  if az acr repository show -n "$REG" --image "$dest" >/dev/null 2>&1; then
    echo "already in ACR: $dest"
  else
    echo ">>> importing docker.io/$src -> $ACR_LOGIN/$dest"
    az acr import -n "$REG" --source "docker.io/$src" --image "$dest"
  fi
}

import_image "$minio_ref"
import_image "$mc_ref"

[ "${1:-}" = "--repoint" ] || { echo "Imported. Run again with --repoint once the cluster is up."; exit 0; }

# MinIO used public images until now, so its namespace has no ACR credentials.
# Same acr-pull secret as vrsky-platform (see deploy-azure.sh), fetched at
# runtime, never stored.
kubectl create secret docker-registry acr-pull \
  --docker-server="$ACR_LOGIN" \
  --docker-username="$(az acr credential show -n "$REG" --query username -o tsv)" \
  --docker-password="$(az acr credential show -n "$REG" --query 'passwords[0].value' -o tsv)" \
  -n "$NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl patch serviceaccount default -n "$NS" -p '{"imagePullSecrets":[{"name":"acr-pull"}]}'

kubectl -n "$NS" set image deploy/minio minio="$(acr_ref "$minio_ref")"
kubectl -n "$NS" rollout status deploy/minio --timeout=300s
kubectl -n "$NS" get pods -l app=minio -o jsonpath='{range .items[*]}{.metadata.name}{"  "}{.spec.containers[0].image}{"\n"}{end}'
