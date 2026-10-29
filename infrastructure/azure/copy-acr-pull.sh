#!/usr/bin/env bash
# copy-acr-pull.sh — refresh the acr-pull copies in vrsky-database (#431) and
# vrsky-ui (#497).
#
# The backup Job in vrsky-database and the UI pods in vrsky-ui pull from ACR
# with a copy of vrsky-platform's acr-pull Secret. deploy-backup-azure.sh and
# deploy-ui-azure.sh make those copies, but only Catch up, a backup change or
# a human runs them, so after an ACR password rotation a copy went stale.
# The deploy job (with the cluster up) and Catch up run this every time,
# before anything rolls, so the first rollout after a rotation already pulls
# with the new password.
#
# Runs inside the cluster through in-cluster.sh: bash, jq and kubectl only,
# no sed, no az. No credential is in this script and it prints none of the
# values (the apply goes through acr-pull-lib.sh).
#
# Never run with -x (bash -x, set -x): the trace prints the Secret.
#
# It touches those Secrets and nothing else. It never reads or applies the
# backup CronJob, so it can't change its `suspend` (a CronJob suspended by
# hand for a restore must stay suspended; see docs/DR.md). That is why the
# deploy job doesn't simply run deploy-backup-azure.sh every time.
#
# A no-op, exit 0, when the source Secret doesn't exist yet (a first
# install). A target namespace that doesn't exist is skipped, and the others
# still get their copy. Any other kubectl error fails.
set -euo pipefail

SRC_NS=vrsky-platform
TARGETS="vrsky-database vrsky-ui"

# shellcheck source=acr-pull-lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/acr-pull-lib.sh"

# Each lookup is a plain assignment, so set -e stops the script when kubectl
# fails (Forbidden, a timeout). Inside `[ -z "$(…)" ]` it would not, and a
# failed lookup would pass for a missing object.
pull="$(kubectl get secret acr-pull -n "$SRC_NS" -o json --ignore-not-found)"
if [ -z "$pull" ]; then
  echo "no acr-pull secret in $SRC_NS yet; nothing to copy into $TARGETS"
  exit 0
fi
for ns in $TARGETS; do
  found="$(kubectl get namespace "$ns" -o name --ignore-not-found)"
  if [ -z "$found" ]; then
    echo "no $ns namespace yet; acr-pull not copied there"
    continue
  fi
  apply_acr_pull "$pull" "$ns"
done
