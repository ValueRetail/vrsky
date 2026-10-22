#!/usr/bin/env bash
# smoke-prod.sh — what the deploy job checks after a rollout, and what you can
# run by hand after one: the public endpoints answer, every restarted
# consumer restored its pipelines, and nothing that matters is firing. What
# matters is decided by smoke-alerts.sh beside this script: VRSky's own
# alerts above info, and anything critical. A fleet restart throttles every
# starting pod for a minute and makes the API server's client calls error
# for a while (CPUThrottlingHigh, KubeClientErrors); neither is a failed
# deploy (plans/smoke-alert-scope.md).
#
# Alerts that count are re-read every SMOKE_ALERT_EVERY seconds for up to
# SMOKE_ALERT_WINDOW (default 15 s / 120 s) before the check fails: a deploy
# that fixes a firing alert applies the rule seconds before this runs, and
# Prometheus serves the old rule until it reloads (~40 s on 2026-10-22, which
# failed the run that shipped the fix). A real problem is still firing after
# two minutes; a clean run reads once (plans/smoke-rule-reload.md).
#   smoke-prod.sh [consumer-deployment …]   e.g. smoke-prod.sh vrsky-webhook-consumer
set -euo pipefail
URL="${PUBLIC_URL:-https://vrsky.valueretail.no}"
KUBECTL="${KUBECTL:-kubectl}" # a test stands a stub in
NS=vrsky-platform
fail=0

code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/readyz"); echo "readyz: $code"; [ "$code" = 200 ] || fail=1
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/");       echo "ui:     $code"; [ "$code" = 200 ] || fail=1

for d in "$@"; do
  for _ in $(seq 1 30); do
    if "$KUBECTL" logs -n "$NS" "deploy/$d" --since=15m 2>/dev/null | grep -q '"msg":"Restored running pipelines"'; then break; fi
    sleep 2
  done
  if "$KUBECTL" logs -n "$NS" "deploy/$d" --since=15m 2>/dev/null | grep -q '"msg":"Restored running pipelines"'; then
    echo "$d: restored its pipelines"
  else
    echo "$d: no 'Restored running pipelines' line within 60 s" >&2; fail=1
  fi
done

alert_window="${SMOKE_ALERT_WINDOW:-120}"
alert_every="${SMOKE_ALERT_EVERY:-15}"
waited=0
while :; do
  firing=$("$KUBECTL" get --raw "/api/v1/namespaces/vrsky-monitoring/services/prometheus-prometheus:9090/proxy/api/v1/rules" 2>/dev/null \
    | bash "$(dirname "${BASH_SOURCE[0]}")/smoke-alerts.sh" 2>/dev/null | tr '\n' ' ' || echo "")
  firing="${firing% }"
  if [ -z "$firing" ] || [ "$waited" -ge "$alert_window" ]; then break; fi
  echo "alerts firing: $firing (re-checking in ${alert_every}s, ${waited}s of ${alert_window}s)"
  sleep "$alert_every"
  waited=$((waited + alert_every))
done
if [ -n "$firing" ]; then echo "alerts firing: $firing" >&2; fail=1; else echo "alerts: none"; fi

[ "$fail" = 0 ] && echo "smoke: ok" || { echo "smoke: FAILED" >&2; exit 1; }
