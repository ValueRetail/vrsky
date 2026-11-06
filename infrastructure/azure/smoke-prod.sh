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

# current_rs <deployment> finds the deployment's current pods: those of the
# ReplicaSet whose revision is the deployment's. It sets cur_rev, cur_sel
# (the selector as k=v,…) and cur_hash (pod-template-hash), and fails when
# it can't tell. `logs deploy/…` picks any one pod, and right after a
# rollout that can be the old one still terminating (#741). Revision, not
# position or age: `get rs` lists by name, and a rollback reuses an older
# ReplicaSet.
current_rs() {
  local out
  cur_rev=""; cur_sel=""; cur_hash=""
  out=$("$KUBECTL" get deploy -n "$NS" "$1" -o 'go-template={{index .metadata.annotations "deployment.kubernetes.io/revision"}} {{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null) || return 1
  cur_rev="${out%% *}"; cur_sel="${out#* }"; cur_sel="${cur_sel%,}"
  if [ -z "$cur_rev" ] || [ "$cur_rev" = "<no value>" ] || [ -z "$cur_sel" ]; then return 1; fi
  cur_hash=$("$KUBECTL" get rs -n "$NS" -l "$cur_sel" -o 'go-template={{range .items}}{{index .metadata.annotations "deployment.kubernetes.io/revision"}} {{index .metadata.labels "pod-template-hash"}}{{"\n"}}{{end}}' 2>/dev/null \
    | awk -v r="$cur_rev" '$1 == r { h = $2 } END { print h }') || return 1
  [ -n "$cur_hash" ]
}

# restored <deployment>: did any current pod log the line in the last 15
# minutes? `logs -l` keeps only 10 lines per pod unless told otherwise, hence
# --tail=-1; --ignore-errors so one pod that can't be read doesn't hide
# another's line. Logs are read whole before grep: `logs | grep -q` under
# pipefail fails on a match when grep exits first and kubectl gets SIGPIPE.
restored() {
  local logs
  current_rs "$1" || return 1
  logs=$("$KUBECTL" logs -n "$NS" -l "$cur_sel,pod-template-hash=$cur_hash" --prefix --since=15m --tail=-1 --ignore-errors 2>/dev/null) || true
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
    echo "$d: no 'Restored running pipelines' line within 60 s (revision ${cur_rev:-?}, selector ${cur_sel:-?}, pod-template-hash ${cur_hash:-?})" >&2; fail=1
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
