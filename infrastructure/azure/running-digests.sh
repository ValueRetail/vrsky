#!/usr/bin/env bash
# running-digests.sh — "name sha256:…" for every VRSky image container running
# in production, one line per distinct pair, sorted (plans/digest-drift.md).
#
# Runs inside the cluster through in-cluster.sh: kubectl and jq only. The name
# is the image repository's last segment (management-api, sitoo-consumer…),
# the digest is what the container actually runs (status.containerStatuses
# imageID), which for a :latest tag is the only place the digest shows.
set -euo pipefail

for ns in vrsky-platform vrsky-ui; do
  kubectl get pods -n "$ns" -o json
done | jq -r '
  .items[]
  | select(.status.phase == "Running")
  | .status.containerStatuses[]?
  | select(.image | contains("/vrsky/"))
  | "\(.image | split("/")[-1] | split("@")[0] | split(":")[0]) \(.imageID | split("@")[1])"
' | sort -u
