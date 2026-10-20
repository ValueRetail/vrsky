#!/usr/bin/env bash
# smoke-alerts.sh — which firing alerts fail a deploy (plans/smoke-alert-scope.md).
#
#   kubectl get --raw .../api/v1/rules | smoke-alerts.sh
#
# Reads Prometheus's /api/v1/rules JSON on stdin and prints, sorted and one
# per line, the firing alerts that count: everything in a vrsky-* rule group
# above severity info, and anything critical from any group. Stock warnings
# that a fleet restart produces by itself (KubeClientErrors, mid-rollout
# replica mismatches) and the chart's info-level noise do not; Watchdog and
# InfoInhibitor sit in general.rules at severity none and fall out by
# construction. Nothing on stdout means the deploy is clean. Exit 0 either
# way; smoke-prod.sh decides. In-cluster runtime: bash and jq only.
set -euo pipefail

jq -r '
  [ .data.groups[]
    | .name as $group
    | .rules[]
    | select(.type == "alerting")
    | select(($group | startswith("vrsky-")) or .labels.severity == "critical")
    | select(.labels.severity != "info")
    | .alerts[]?
    | select(.state == "firing")
    | .labels.alertname
  ] | unique | .[]'
