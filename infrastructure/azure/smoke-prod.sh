#!/usr/bin/env bash
# smoke-prod.sh — what the deploy job checks after a rollout, and what you can
# run by hand after one: the public endpoints answer, every restarted
# consumer restored its pipelines, and nothing that matters is firing. What
# matters is decided by smoke-alerts.sh beside this script: VRSky's own
# alerts above info, and anything critical. A fleet restart throttles every
# starting pod for a minute and makes the API server's client calls error
# for a while (CPUThrottlingHigh, KubeClientErrors); neither is a failed
# deploy (plans/smoke-alert-scope.md).
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

firing=$(kubectl get --raw "/api/v1/namespaces/vrsky-monitoring/services/prometheus-prometheus:9090/proxy/api/v1/rules" 2>/dev/null \
  | bash "$(dirname "${BASH_SOURCE[0]}")/smoke-alerts.sh" 2>/dev/null | tr '\n' ' ' || echo "")
firing="${firing% }"
if [ -n "$firing" ]; then echo "alerts firing: $firing" >&2; fail=1; else echo "alerts: none"; fi

[ "$fail" = 0 ] && echo "smoke: ok" || { echo "smoke: FAILED" >&2; exit 1; }
