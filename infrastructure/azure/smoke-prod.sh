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

# current_pod_logs <deployment> prints the last 15 minutes of logs of the
# deployment's current pods: those of the ReplicaSet whose revision is the
# deployment's. `logs deploy/…` picks any one pod, and right after a rollout
# that can be the old one still terminating (#741). Revision rather than
# creation time, because a rollback reuses an older ReplicaSet. `logs -l`
# keeps only 10 lines per pod unless told otherwise, hence --tail=-1.
current_pod_logs() {
  local d="$1" out rev sel hash
  out=$("$KUBECTL" get deploy -n "$NS" "$d" -o 'go-template={{index .metadata.annotations "deployment.kubernetes.io/revision"}} {{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null) || return 1
  rev="${out%% *}"; sel="${out#* }"; sel="${sel%,}"
  if [ -z "$rev" ] || [ "$rev" = "<no value>" ] || [ -z "$sel" ]; then return 1; fi
  hash=$("$KUBECTL" get rs -n "$NS" -l "$sel" -o 'go-template={{range .items}}{{index .metadata.annotations "deployment.kubernetes.io/revision"}} {{index .metadata.labels "pod-template-hash"}}{{"\n"}}{{end}}' 2>/dev/null \
    | awk -v r="$rev" '$1 == r { h = $2 } END { print h }') || return 1
  [ -n "$hash" ] || return 1
  "$KUBECTL" logs -n "$NS" -l "$sel,pod-template-hash=$hash" --prefix --since=15m --tail=-1 --max-log-requests=10 2>/dev/null
}

# Logs are read whole before grep: `logs | grep -q` under pipefail fails on a
# match when grep exits first and kubectl gets SIGPIPE. What a partly failed
# read printed still counts.
restored() {
  local logs
  logs=$(current_pod_logs "$1") || true
  grep -q '"msg":"Restored running pipelines"' <<<"$logs"
}

restore_tries="${SMOKE_RESTORE_TRIES:-30}" # 2 s apart; a test shortens it
for d in "$@"; do
  for _ in $(seq 1 "$restore_tries"); do
    if restored "$d"; then break; fi
    sleep 2
  done
  if restored "$d"; then
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
