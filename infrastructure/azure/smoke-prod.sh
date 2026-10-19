#!/usr/bin/env bash
# smoke-prod.sh — what the deploy job checks after a rollout, and what you can
# run by hand after one: the public endpoints answer, every restarted
# consumer restored its pipelines, and nothing new is firing.
#   smoke-prod.sh [consumer-deployment …]   e.g. smoke-prod.sh vrsky-webhook-consumer
set -euo pipefail
URL="${PUBLIC_URL:-https://vrsky.valueretail.no}"
NS=vrsky-platform
fail=0

code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/readyz"); echo "readyz: $code"; [ "$code" = 200 ] || fail=1
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/");       echo "ui:     $code"; [ "$code" = 200 ] || fail=1

for d in "$@"; do
  for _ in $(seq 1 30); do
    if kubectl logs -n "$NS" "deploy/$d" --since=15m 2>/dev/null | grep -q '"msg":"Restored running pipelines"'; then break; fi
    sleep 2
  done
  if kubectl logs -n "$NS" "deploy/$d" --since=15m 2>/dev/null | grep -q '"msg":"Restored running pipelines"'; then
    echo "$d: restored its pipelines"
  else
    echo "$d: no 'Restored running pipelines' line within 60 s" >&2; fail=1
  fi
done

firing=$(kubectl get --raw "/api/v1/namespaces/vrsky-monitoring/services/prometheus-prometheus:9090/proxy/api/v1/alerts" 2>/dev/null \
  | jq -r '[.data.alerts[] | select(.state=="firing") | .labels.alertname | select(. != "Watchdog" and . != "InfoInhibitor")] | sort | join(" ")' 2>/dev/null || echo "")
if [ -n "$firing" ]; then echo "alerts firing: $firing" >&2; fail=1; else echo "alerts: none"; fi

[ "$fail" = 0 ] && echo "smoke: ok" || { echo "smoke: FAILED" >&2; exit 1; }
