#!/usr/bin/env bash
# build-push-acr.sh — build the VRSky container images in Azure Container
# Registry (ACR) using `az acr build` (server-side, linux/amd64 — no local
# Docker needed, and no Mac-ARM cross-build headaches).
#
# Every Dockerfile uses the REPO ROOT as its build context, so this script
# must be run from the repo root (it cd's there itself). The root
# .dockerignore trims the uploaded context to just src/ and ui/.
#
# Usage:
#   infrastructure/azure/build-push-acr.sh [group]
#     group = core | workers | connectors | all   (default: all)
#
#   REG=myregistry infrastructure/azure/build-push-acr.sh core   # override ACR name
#
# Groups:
#   core       management-api, ui, data-filter, data-converter (running platform)
#   workers    the generic per-node images the orchestrator spins up:
#              vrsky-consumer / vrsky-producer
#              (image pattern must match orchestrator/types.go: {registry}/vrsky-{nodeType};
#              filter/converter nodes are served by the shared data-* services, #201)
#   connectors the standing retail connectors (Sitoo, Front Systems, Business
#              Central, Visma, Brightpearl) — built ready; they still need
#              Kubernetes Deployment manifests before they can run on AKS.
set -euo pipefail

REG="${REG:-vrskyprodacr}"                 # ACR name (not the login server)
GROUP="${1:-all}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# build <acr-repo:tag> <dockerfile>
build() {
  local tag="$1" dockerfile="$2"
  echo ""
  echo ">>> az acr build  $tag   (-f $dockerfile)"
  az acr build --registry "$REG" --image "$tag" --file "$dockerfile" .
}

build_core() {
  build vrsky/management-api:latest src/cmd/management-api/Dockerfile
  build vrsky/ui:latest             ui/Dockerfile
  # The pipeline transforms are the SHARED data-filter/data-converter services
  # (#201) — the same binaries dev/TEST runs, with the claim-check + record
  # streaming (ADR 0002). The legacy cmd/filter + cmd/converter images they
  # replace were wired to topics nothing publishes to.
  build vrsky/data-filter:latest    src/cmd/data-filter/Dockerfile
  build vrsky/data-converter:latest src/cmd/data-converter/Dockerfile
}

build_workers() {
  # NOTE the `vrsky-` prefix in the repo name — the orchestrator resolves
  # per-node images as {WORKER_IMAGE_REGISTRY}/vrsky-{nodeType}:{version}.
  # No vrsky-filter/vrsky-converter: the orchestrator no longer spawns
  # per-connection transform workers (#201) — the shared data-filter and
  # data-converter services (build_core) handle those nodes.
  build vrsky/vrsky-consumer:latest  src/cmd/consumer/Dockerfile
  build vrsky/vrsky-producer:latest  src/cmd/producer/Dockerfile
}

build_connectors() {
  for c in sitoo front-systems business-central visma brightpearl; do
    build "vrsky/${c}-consumer:latest" "src/cmd/${c}-consumer/Dockerfile"
    build "vrsky/${c}-producer:latest" "src/cmd/${c}-producer/Dockerfile"
  done
}

echo "Registry : $REG"
echo "Group    : $GROUP"
echo "Context  : $REPO_ROOT (trimmed by .dockerignore)"

case "$GROUP" in
  core)       build_core ;;
  workers)    build_workers ;;
  connectors) build_connectors ;;
  all)        build_core; build_workers; build_connectors ;;
  *) echo "unknown group '$GROUP' (use: core | workers | connectors | all)" >&2; exit 2 ;;
esac

echo ""
echo "Done. Images in $REG:"
echo "  az acr repository list -n $REG -o table"
