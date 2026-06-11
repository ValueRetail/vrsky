#!/usr/bin/env bash
# Tenant-publish load generator: publish N envelopes onto a source tenant's
# pipeline subject (vrsky.data.<tenant>.pipeline.<conn>) straight over NATS via
# the nats-box CLI, and report the achieved publish rate.
#
# This drives the data-plane side of the cross-tenant scenario. NOTE: measuring
# *delivery* through the tenant-consumer bridge to a destination tenant also
# requires an approved tenant_data_connection and a running bridge — that
# control-plane setup (the data-sharing approval flow) is out of scope for the
# local harness and is captured on a fixed cluster (#90). Here we baseline how
# fast envelopes can be pushed into a tenant's stream.
#
# Usage: tenant_publish.sh [--count N | --rate R --duration S]
#                          [--tenant ID] [--conn ID]
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/load/lib.sh
source "$HERE/../lib.sh"

COUNT=50000
RATE=""; DURATION=""
TENANT="load-src-tenant"
CONN="load-src-pipeline"
NATS_INTERNAL="${NATS_INTERNAL:-nats://nats:4222}"
NATS_BOX_IMAGE="${NATS_BOX_IMAGE:-natsio/nats-box:latest}"
while [ $# -gt 0 ]; do
  case "$1" in
    --count) COUNT="$2"; shift 2;;
    --rate) RATE="$2"; shift 2;;
    --duration) DURATION="$2"; shift 2;;
    --tenant) TENANT="$2"; shift 2;;
    --conn) CONN="$2"; shift 2;;
    *) shift;;
  esac
done
if [ -n "$RATE" ] && [ -n "$DURATION" ]; then
  secs="${DURATION%s}"; secs="${secs%m}"; [ "${DURATION: -1}" = "m" ] && secs=$((secs*60))
  COUNT=$((RATE * secs))
fi

need docker
subject="vrsky.data.${TENANT}.pipeline.${CONN}"
log "publishing $COUNT envelopes to $subject via nats-box…"
t0=$(date +%s.%N)
docker run --rm --network "$DOCKER_NETWORK" "$NATS_BOX_IMAGE" \
  nats pub --server "$NATS_INTERNAL" "$subject" \
  '{"event":"tenant.publish","seq":{{Count}},"ts":"{{TimeStamp}}"}' \
  --count "$COUNT" >/dev/null 2>&1
t1=$(date +%s.%N)
secs=$(awk -v a="$t1" -v b="$t0" 'BEGIN{printf "%.2f", a-b}')
rate=$(awk -v n="$COUNT" -v s="$secs" 'BEGIN{ if(s>0) printf "%.0f", n/s; else print "n/a" }')

echo
ok "tenant-publish: pushed $COUNT envelopes in ${secs}s (~${rate} msg/s into $subject)"
log "delivery through the tenant-consumer bridge needs an approved data connection — see docs/LOAD.md."
