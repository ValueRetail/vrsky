#!/usr/bin/env bash
# copy-acr-pull.sh — refresh the acr-pull copy in vrsky-database (#431).
#
# The backup Job in vrsky-database pulls vrsky-cli from ACR with a copy of
# vrsky-platform's acr-pull Secret. deploy-backup-azure.sh makes that copy,
# but only Catch up and a backup change run it, so after an ACR password
# rotation the copy went stale until the next Catch up. The deploy job now
# runs this on every deploy as well.
#
# Runs inside the cluster through in-cluster.sh: bash, jq and kubectl only,
# no sed, no az. No credential is in this script and it prints none of the
# values.
#
# It touches that one Secret and nothing else. It never reads or applies
# the backup CronJob, so it can't change its `suspend` (a CronJob suspended
# by hand for a restore must stay suspended; see docs/DR.md). That is why
# the deploy job doesn't simply run deploy-backup-azure.sh every time.
#
# A no-op, exit 0, when the source Secret or the target namespace doesn't
# exist yet (a first install). Any other kubectl error fails.
set -euo pipefail

SRC_NS=vrsky-platform
NS=vrsky-database

pull="$(kubectl get secret acr-pull -n "$SRC_NS" -o json --ignore-not-found)"
if [ -z "$pull" ]; then
  echo "no acr-pull secret in $SRC_NS yet; nothing to copy into $NS"
  exit 0
fi
if [ -z "$(kubectl get namespace "$NS" -o name --ignore-not-found)" ]; then
  echo "no $NS namespace yet; acr-pull not copied"
  exit 0
fi

# The source's identity (uid, resourceVersion, ownerReferences, annotations)
# stays behind: apply would refuse a foreign uid.
printf '%s' "$pull" \
  | jq --arg ns "$NS" '{apiVersion: "v1", kind: "Secret", type: .type, data: .data,
                        metadata: {name: "acr-pull", namespace: $ns}}' \
  | kubectl apply -f -
