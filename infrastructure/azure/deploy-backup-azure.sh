#!/usr/bin/env bash
# deploy-backup-azure.sh — apply the management-DB backup CronJob (#395).
#
# Runs inside the cluster through in-cluster.sh (the deploy job and Catch up),
# from a directory that holds infrastructure/ with its paths intact. The
# runtime has bash, jq and kubectl; no sed, no az.
#
#   1. Copy the acr-pull secret from vrsky-platform into vrsky-database, so the
#      Job can pull vrsky-cli from ACR. Copied on every run, so a rotated ACR
#      password follows. No credential is in this script, and a failed apply
#      prints none (acr-pull-lib.sh).
#   2. Decide `suspend`: false only when the vrsky-backup-config Secret exists
#      with the keys a backup needs (REQUIRED below, plus Azure credentials:
#      account name and key, or a connection string). Otherwise it stays
#      suspended and this says why: a Job that fails every weekday until
#      someone creates the Secret helps nobody.
#   3. Apply infrastructure/kubernetes/backup/cronjob.yaml (suspended in the
#      file) with that value, in one apply.
#
# vrsky-backup-config is created by a human with `kubectl create secret
# generic` (infrastructure/kubernetes/backup/README.md). This script reads its
# key names and nothing else: it never creates, changes or prints it.
set -euo pipefail

NS=vrsky-database
CRONJOB=vrsky-mgmt-backup
CONFIG=vrsky-backup-config
MANIFEST=infrastructure/kubernetes/backup/cronjob.yaml
REQUIRED="BACKUP_DB_URL ENCRYPTION_KEY BACKUP_PROVIDER BACKUP_BUCKET"

# --- 1. acr-pull ------------------------------------------------------------
if ! pull="$(kubectl get secret acr-pull -n vrsky-platform -o json)"; then
  echo "ERROR: no acr-pull secret in vrsky-platform to copy; the backup Job could not pull its image" >&2
  exit 1
fi
# The apply's output is captured: a failure can print the Secret (#496).
# shellcheck source=acr-pull-lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/acr-pull-lib.sh"
apply_acr_pull "$pull" "$NS"

# --- 2. suspended unless the config Secret is complete ------------------------
# Key names only; the values never leave the API server.
missing=""
if keys="$(kubectl get secret "$CONFIG" -n "$NS" -o json 2>/dev/null | jq -r '.data // {} | keys[]')"; then
  nl=$'\n'
  has() { case "$nl$keys$nl" in *"$nl$1$nl"*) return 0 ;; esac; return 1; }
  for k in $REQUIRED; do
    has "$k" || missing="$missing $k"
  done
  # Azure credentials: an account name and key, or a connection string.
  if ! { has BACKUP_AZURE_ACCOUNT_NAME && has BACKUP_AZURE_ACCOUNT_KEY; } && ! has BACKUP_AZURE_CONNECTION_STRING; then
    missing="$missing BACKUP_AZURE_ACCOUNT_NAME+BACKUP_AZURE_ACCOUNT_KEY or BACKUP_AZURE_CONNECTION_STRING"
  fi
  [ -z "$missing" ] || reason="Secret $NS/$CONFIG lacks:$missing"
else
  missing=all
  reason="Secret $NS/$CONFIG does not exist"
fi

suspend=true
[ -n "$missing" ] || suspend=false

# --- 3. the CronJob, with its final suspend value -----------------------------
# One apply. Applying the file (suspend: true) and patching afterwards would
# suspend a live CronJob for a moment on every deploy, and that one sample
# would mute MgmtBackupMissing. `patch --local` edits the manifest client-side.
kubectl patch --local -f "$MANIFEST" --type merge -p "{\"spec\":{\"suspend\":$suspend}}" -o yaml \
  | kubectl apply -f -

if [ "$suspend" = false ]; then
  echo "backup CronJob $NS/$CRONJOB applied and active (weekdays 12:00 Europe/Oslo)"
else
  echo "backup CronJob $NS/$CRONJOB applied SUSPENDED: $reason."
  echo "  Create it as in infrastructure/kubernetes/backup/README.md, then run Catch up"
  echo "  (or this script) to activate the backups."
fi
