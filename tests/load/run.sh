#!/usr/bin/env bash
# VRSky load-test harness — orchestrates a capacity scenario end to end:
# bootstraps auth, deploys the pipeline, drives load, measures p99 latency
# (k6) + sustained throughput (the vrsky_messages_published_total counter), and
# prints a one-line result. See docs/LOAD.md for measured baselines.
#
# Usage:
#   tests/load/run.sh [options] <scenario>
#
# Scenarios:
#   webhook-to-http   k6 → webhook ingress → NATS → http-producer → httpbin  (flagship)
#   db-cdc            bulk INSERT → postgres-consumer CDC → http-producer
#   file-to-db        multipart uploads → file-consumer → db-producer
#   tenant-to-tenant  publish → source tenant → tenant-consumer bridge
#   all               run every scenario in sequence
#
# Options:
#   --rate N        target msgs/sec        (default 200)
#   --duration S    load duration          (default 30s; e.g. 15s, 1m)
#   --p99-ceiling N fail if p99 > N ms      (default 0 = no threshold; used by CI smoke)
#   --keep          leave the deployed pipeline running (default: stop it)
#   -h | --help     this help
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/load/lib.sh
source "$HERE/lib.sh"

RATE=200
DURATION=30s
P99_CEILING=0
KEEP=0

usage() { sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

SCENARIO=""
while [ $# -gt 0 ]; do
  case "$1" in
    --rate) RATE="$2"; shift 2;;
    --duration) DURATION="$2"; shift 2;;
    --p99-ceiling) P99_CEILING="$2"; shift 2;;
    --keep) KEEP=1; shift;;
    -h|--help) usage 0;;
    -*) err "unknown option: $1"; usage 1;;
    *) SCENARIO="$1"; shift;;
  esac
done
[ -n "$SCENARIO" ] || { err "no scenario given"; usage 1; }

# ---- duration → seconds (for throughput math) ----
duration_seconds() {
  local d="$1"
  case "$d" in
    *m) echo $(( ${d%m} * 60 ));;
    *s) echo "${d%s}";;
    *) echo "$d";;
  esac
}

# ---------------------------------------------------------------------------
# Flagship: webhook → http
# ---------------------------------------------------------------------------
run_webhook_to_http() {
  local tid cid before after secs delivered p99 rate failed
  log "bootstrapping auth…"
  tid="$(bootstrap_auth)"; [ -n "$tid" ] || exit 1
  ok "tenant=$tid"

  local nodes='[
    {"id":"wh","type":"consumer","config":{"type":"http"},"enabled":true},
    {"id":"hp","type":"producer","config":{"type":"http","http":{"url":"http://httpbin/post","method":"POST"}},"enabled":true}
  ]'
  local edges='[{"id":"e1","source":"wh","target":"hp","order":0}]'
  log "deploying webhook→http pipeline…"
  cid="$(deploy_pipeline "$tid" "load-webhook-http-$(date +%s)-$RANDOM" "$nodes" "$edges")" || exit 1
  ok "connection=$cid"
  # Give the webhook-consumer a moment to register the /webhook/{id} route.
  sleep 3

  before="$(prom_value "vrsky_messages_published_total{tenant_id=\"$tid\"}")"

  local outdir; outdir="$(mktemp -d)"
  log "running k6: rate=$RATE/s duration=$DURATION (p99 ceiling=${P99_CEILING}ms)"
  set +e
  docker run --rm --network "$DOCKER_NETWORK" \
    -e WEBHOOK_URL="$WEBHOOK_INGRESS_INTERNAL/webhook/$cid" \
    -e RATE="$RATE" -e DURATION="$DURATION" \
    -e P99_CEILING_MS="$P99_CEILING" \
    -e MIN_RATE="${MIN_RATE:-0}" \
    -e MAX_FAILED_RATE="${MAX_FAILED_RATE:-0.01}" \
    -v "$outdir:/out" \
    -i "$K6_IMAGE" run - < "$HERE/webhook_to_http.js"
  local k6_rc=$?
  set -e

  after="$(prom_value "vrsky_messages_published_total{tenant_id=\"$tid\"}")"
  secs="$(duration_seconds "$DURATION")"

  if [ -f "$outdir/summary.json" ]; then
    p99="$(jq -r '((.p99 // 0)*10|round)/10' "$outdir/summary.json")"
    rate="$(jq -r '(.rate // 0)|round' "$outdir/summary.json")"
    failed="$(jq -r '((.failed_rate // 0)*100*100|round)/100' "$outdir/summary.json")"
  else
    p99="n/a"; rate="n/a"; failed="n/a"
  fi
  # NATS-confirmed published delta (cross-check; coarse due to 15s scrape).
  delivered="$(awk -v a="$after" -v b="$before" 'BEGIN{printf "%d", a-b}')"
  local published_rate; published_rate="$(awk -v d="$delivered" -v s="$secs" 'BEGIN{ if(s>0) printf "%.0f", d/s; else print 0 }')"
  rm -rf "$outdir"

  echo
  result_row "webhook→http" "$p99" "$rate" "$delivered" "${failed}%"
  log "NATS-confirmed published delta: $delivered msgs over ${secs}s (~${published_rate}/s; coarse — 15s scrape)"
  [ "$KEEP" -eq 1 ] || stop_pipeline "$tid" "$cid"
  return "$k6_rc"
}

# ---------------------------------------------------------------------------
# The non-HTTP scenarios are driven by the generators in generators/. They are
# self-contained and measure the relevant Prometheus counter delta themselves.
# ---------------------------------------------------------------------------
run_db_cdc()          { "$HERE/generators/cdc_insert.sh"   --rate "$RATE" --duration "$DURATION"; }
run_file_to_db()      { "$HERE/generators/file_drop.sh"    --rate "$RATE" --duration "$DURATION"; }
run_tenant_to_tenant(){ "$HERE/generators/tenant_publish.sh" --rate "$RATE" --duration "$DURATION"; }

# Always purge the data stream on the way out so a run never leaves a durable
# backlog behind (which has OOM-killed NATS). Set after arg parsing so --help
# doesn't trigger it; best-effort, preserves the scenario's exit code.
trap purge_data_stream EXIT

case "$SCENARIO" in
  webhook-to-http)  run_webhook_to_http;;
  db-cdc)           run_db_cdc;;
  file-to-db)       run_file_to_db;;
  tenant-to-tenant) run_tenant_to_tenant;;
  all)
    run_webhook_to_http || warn "webhook-to-http exited non-zero"
    run_db_cdc          || warn "db-cdc exited non-zero"
    run_file_to_db      || warn "file-to-db exited non-zero"
    run_tenant_to_tenant|| warn "tenant-to-tenant exited non-zero"
    ;;
  *) err "unknown scenario: $SCENARIO"; usage 1;;
esac
