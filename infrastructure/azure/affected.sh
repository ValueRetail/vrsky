#!/usr/bin/env bash
# affected.sh — which production targets a set of changed paths touches.
#
# Reads changed paths on stdin (one per line: `git diff --name-only A B`) and
# prints one target per line, sorted, unique:
#   image <name>          an image to rebuild in ACR (vrsky/<name>:latest)
#   core <name>           a core service to roll with deploy-core-azure.sh
#   connector <name>      a connector deployment to restart
#   connectors-manifest   the connector manifest changed: full deploy-connectors-azure.sh
#   monitoring            alert rules or monitoring values changed: install-monitoring.sh
#   backup                the backup CronJob or its deploy script changed:
#                         deploy-backup-azure.sh
#
# TOOLS are images with nothing to roll: the backup CronJob pulls
# vrsky-cli:latest at every run, so a rebuild is the whole deploy.
#
# The deploy job in .github/workflows/build-push.yml feeds it a merge's diff.
# Pure text in, text out, so TestAffectedServices drives it with path lists.
# TestAffectedServicesKnowsEveryImage keeps the lists below equal to what
# build-push-acr.sh builds and deploy-core-azure.sh deploys.
set -euo pipefail

CORE="management-api ui data-filter data-converter alert-responder"
CONNECTORS="sitoo-consumer sitoo-producer front-systems-consumer front-systems-producer
business-central-consumer business-central-producer visma-consumer visma-producer
brightpearl-consumer brightpearl-producer sap-s4hana-consumer sap-s4hana-producer
api-consumer webhook-consumer file-consumer db-consumer tenant-consumer
cloud-storage-consumer sftp-consumer kafka-consumer rabbitmq-consumer
salesforce-consumer remote-agent
http-producer db-producer file-producer cloud-storage-producer
sftp-producer kafka-producer rabbitmq-producer salesforce-producer"
TOOLS="vrsky-cli"

has() { case " $1 " in *" $2 "*) return 0 ;; esac; return 1; }

out=()
all_go=0
while IFS= read -r p; do
  [ -z "$p" ] && continue
  case "$p" in
    ui/*)                                   out+=("image ui" "core ui") ;;
    src/pkg/*|src/go.mod|src/go.sum)        all_go=1 ;;
    src/cmd/*/Dockerfile)                   all_go=1 ;;
    src/cmd/*)
      name="${p#src/cmd/}"; name="${name%%/*}"
      if has "$CORE" "$name"; then out+=("image $name" "core $name")
      elif has "$CONNECTORS" "$name"; then out+=("image $name" "connector $name")
      elif has "$TOOLS" "$name"; then out+=("image $name")
      fi ;;
    infrastructure/prometheus-rules.yml|infrastructure/kubernetes/monitoring/*)
                                            out+=("monitoring") ;;
    infrastructure/azure/deploy-connectors-azure.sh|infrastructure/kubernetes/connectors/*)
                                            out+=("connectors-manifest") ;;
    infrastructure/azure/deploy-backup-azure.sh|infrastructure/kubernetes/backup/*)
                                            out+=("backup") ;;
  esac
done

if [ "$all_go" = 1 ]; then
  for n in $CORE; do [ "$n" = ui ] && continue; out+=("image $n" "core $n"); done
  for n in $CONNECTORS; do out+=("image $n" "connector $n"); done
  for n in $TOOLS; do out+=("image $n"); done
fi

[ ${#out[@]} -eq 0 ] && exit 0
printf '%s\n' "${out[@]}" | sort -u
