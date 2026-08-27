#!/usr/bin/env bash
# k3d-load-images.sh — build VRSky service images from THIS checkout and load
# them into the local k3d cluster, so the k8s manifests (which reference private
# GHCR / a localhost:5000 registry the cluster can't reach) run with locally
# built images. The deployments use imagePullPolicy: IfNotPresent, so an
# imported image with a matching tag is used without any registry pull.
#
# Usage:
#   infrastructure/scripts/k3d-load-images.sh [core|connectors|all] [cluster-name]
#
#   core       (default) management-api + the shared transforms — what
#              deploy-vrsky-platform.sh needs
#   connectors the standing connector services that run pipeline source and
#              destination nodes
#   all        core + connectors
#
# There is no "workers" scope: the orchestrator stopped spawning per-connection
# worker pods in #201 (transforms) and #205 (edges), so the vrsky-{consumer,
# producer,filter,converter} images nothing pulls are no longer built.
#
# Run from the repo root. Requires docker + k3d, and the k3d cluster to exist.
set -euo pipefail

SCOPE="${1:-core}"
CLUSTER="${2:-vrsky-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

# image-ref  cmd-dir  — refs match exactly what the k8s manifests pull.
CORE=(
  "ghcr.io/valueretail/vrsky/management-api:latest  management-api"
  "localhost:5000/vrsky/data-filter:latest          data-filter"
  "localhost:5000/vrsky/data-converter:latest       data-converter"
)
# Standing connector services: one per source/destination node type. These are
# what actually run a pipeline's edges (#205).
CONNECTORS=(
  "localhost:5000/vrsky/file-consumer:latest           file-consumer"
  "localhost:5000/vrsky/webhook-consumer:latest        webhook-consumer"
  "localhost:5000/vrsky/api-consumer:latest            api-consumer"
  "localhost:5000/vrsky/db-consumer:latest             db-consumer"
  "localhost:5000/vrsky/cloud-storage-consumer:latest  cloud-storage-consumer"
  "localhost:5000/vrsky/http-producer:latest           http-producer"
  "localhost:5000/vrsky/file-producer:latest           file-producer"
  "localhost:5000/vrsky/db-producer:latest             db-producer"
  "localhost:5000/vrsky/cloud-storage-producer:latest  cloud-storage-producer"
)

build_and_import() {
  local ref="$1" dir="$2"
  local dockerfile="src/cmd/${dir}/Dockerfile"
  if [ ! -f "$dockerfile" ]; then
    echo "  ✗ no Dockerfile at $dockerfile — skipping $ref" >&2
    return 1
  fi
  echo "  → building $ref (from $dockerfile)"
  docker build -q -f "$dockerfile" -t "$ref" . >/dev/null
  echo "  → importing into k3d cluster '$CLUSTER'"
  k3d image import "$ref" -c "$CLUSTER" >/dev/null
  echo "  ✓ $ref"
}

if ! k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
  echo "k3d cluster '$CLUSTER' not found — create it first:" >&2
  echo "  k3d cluster create --config infrastructure/kubernetes/k3d-config.yaml" >&2
  exit 2
fi

# Select the entries for the chosen scope (bash 3.2-compatible — no namerefs).
case "$SCOPE" in
  core)       SETS=("${CORE[@]}") ;;
  connectors) SETS=("${CONNECTORS[@]}") ;;
  all)        SETS=("${CORE[@]}" "${CONNECTORS[@]}") ;;
  *) echo "unknown scope '$SCOPE' (use core|connectors|all)" >&2; exit 2 ;;
esac

echo "Loading images (scope: $SCOPE) into k3d cluster '$CLUSTER'..."
for entry in "${SETS[@]}"; do
  # shellcheck disable=SC2086
  build_and_import $entry
done

echo
echo "Done. Restart any already-deployed pods to pick up the imported images, e.g.:"
echo "  kubectl rollout restart deploy/vrsky-management-api -n vrsky-platform"
echo "  kubectl rollout restart deploy/vrsky-filter         -n vrsky-platform"
