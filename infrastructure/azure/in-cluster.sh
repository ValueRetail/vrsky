#!/usr/bin/env bash
# in-cluster.sh — run a command inside the production cluster through Azure's
# command runner (`az aks command invoke`), so the API server is never reached
# from outside its authorized IP ranges (plans/auto-deploy.md).
#
#   infrastructure/azure/in-cluster.sh <dir> <command…>
#
# <dir> is uploaded whole and becomes the working directory of the command;
# the runtime has bash, curl, jq, kubectl and helm, and no `az`. The command's
# output is printed and its exit code is this script's exit code.
set -euo pipefail
[ $# -ge 2 ] || { echo "usage: in-cluster.sh <dir> <command…>" >&2; exit 2; }
dir="$1"; shift
RG="${AKS_RESOURCE_GROUP:-vrsky-prod}"
CLUSTER="${AKS_CLUSTER:-vrsky-prod}"
result="$(cd "$dir" && az aks command invoke --resource-group "$RG" --name "$CLUSTER" --file . --command "$*" -o json)"
printf '%s\n' "$result" | jq -r '.logs // ""'
code="$(printf '%s' "$result" | jq -r '.exitCode // 1')"
[ "$code" = 0 ] || { echo "in-cluster: command exited $code" >&2; exit "$code"; }
