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
#     group = core | connectors | all   (default: all)
#
#   REG=myregistry infrastructure/azure/build-push-acr.sh core   # override ACR name
#
# Groups:
#   core       management-api, ui, data-filter, data-converter (running platform)
#   connectors the standing connectors — retail/ERP (Sitoo, Front Systems,
#              Business Central, Visma, Brightpearl) and generic (file, HTTP,
#              database, cloud storage, SFTP, Kafka, RabbitMQ, Salesforce,
#              webhook, API, tenant). Deploy them with deploy-connectors-azure.sh.
#
# There is no "workers" group any more: since #201 (transforms) and #205 (edges)
# the orchestrator spawns no per-connection worker pods, so the vrsky-consumer /
# vrsky-producer images it used to resolve have no consumer. Every node kind is
# served by a standing connector service instead.
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
  # streaming (ADR 0002). The legacy cmd/filter + cmd/converter they replaced
  # were wired to topics nothing publishes to, and have since been deleted.
  build vrsky/data-filter:latest    src/cmd/data-filter/Dockerfile
  build vrsky/data-converter:latest src/cmd/data-converter/Dockerfile
}

build_connectors() {
  # Retail/ERP: both directions.
  for c in sitoo front-systems business-central visma brightpearl sap-s4hana; do
    build "vrsky/${c}-consumer:latest" "src/cmd/${c}-consumer/Dockerfile"
    build "vrsky/${c}-producer:latest" "src/cmd/${c}-producer/Dockerfile"
  done
  # Generic source/destination types. Every name here and in the retail loop
  # above must appear in the service table in deploy-connectors-azure.sh, and
  # vice versa: a service with no image ImagePullBackOffs, and an image no
  # service uses is wasted build time. TestConnectorImagesAreBuilt enforces it.
  local generic=(
    api-consumer webhook-consumer file-consumer db-consumer tenant-consumer
    cloud-storage-consumer sftp-consumer kafka-consumer rabbitmq-consumer
    salesforce-consumer
    http-producer db-producer file-producer cloud-storage-producer
    sftp-producer kafka-producer rabbitmq-producer salesforce-producer
  )
  for c in "${generic[@]}"; do
    build "vrsky/${c}:latest" "src/cmd/${c}/Dockerfile"
  done
}

echo "Registry : $REG"
echo "Group    : $GROUP"
echo "Context  : $REPO_ROOT (trimmed by .dockerignore)"

case "$GROUP" in
  core)       build_core ;;
  connectors) build_connectors ;;
  all)        build_core; build_connectors ;;
  *) echo "unknown group '$GROUP' (use: core | connectors | all)" >&2; exit 2 ;;
esac

echo ""
echo "Done. Images in $REG:"
echo "  az acr repository list -n $REG -o table"
