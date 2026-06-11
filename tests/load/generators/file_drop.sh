#!/usr/bin/env bash
# File-ingest load generator: deploy a file → db pipeline, push N multipart
# uploads at the file-consumer's /upload/{id} ingress, and measure the messages
# published onto NATS (vrsky_messages_published_total for the load tenant).
#
# Requires the file-consumer and db-producer services to be up:
#   docker compose up -d nats postgres-management management-api \
#     file-consumer db-producer postgres-target prometheus
#
# Usage: file_drop.sh [--count N | --rate R --duration S]
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/load/lib.sh
source "$HERE/../lib.sh"

COUNT=500
RATE=""; DURATION=""
while [ $# -gt 0 ]; do
  case "$1" in
    --count) COUNT="$2"; shift 2;;
    --rate) RATE="$2"; shift 2;;
    --duration) DURATION="$2"; shift 2;;
    *) shift;;
  esac
done
if [ -n "$RATE" ] && [ -n "$DURATION" ]; then
  secs="${DURATION%s}"; secs="${secs%m}"; [ "${DURATION: -1}" = "m" ] && secs=$((secs*60))
  COUNT=$((RATE * secs))
fi

need curl; need jq; need docker
log "bootstrapping auth…"
tid="$(bootstrap_auth)"; [ -n "$tid" ] || exit 1
ok "tenant=$tid"

nodes='[
  {"id":"fc","type":"consumer","config":{"type":"file"},"enabled":true},
  {"id":"dp","type":"producer","config":{"type":"database","database":{"host":"postgres-target","port":5432,"user":"postgres","password":"target_password","database":"target_db","sslmode":"disable","table":"load_files","mode":"create_insert"}},"enabled":true}
]'
edges='[{"id":"e1","source":"fc","target":"dp","order":0}]'
log "deploying file→db pipeline…"
cid="$(deploy_pipeline "$tid" "load-file-db-$(date +%s)-$RANDOM" "$nodes" "$edges")" || exit 1
ok "connection=$cid"
sleep 3

tmp="$(mktemp)"; echo '{"event":"file.load","amount":42,"currency":"GBP"}' > "$tmp"
before="$(prom_value "vrsky_messages_published_total{tenant_id=\"$tid\"}")"
log "uploading $COUNT files to $FILE_INGRESS/upload/$cid…"
t0=$(date +%s.%N)
ok_count=0
for i in $(seq 1 "$COUNT"); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -F "file=@$tmp;filename=evt-$i.json" "$FILE_INGRESS/upload/$cid")
  [ "$code" = "200" ] || [ "$code" = "202" ] && ok_count=$((ok_count+1))
done
t1=$(date +%s.%N)
rm -f "$tmp"
sleep 5
after="$(prom_value "vrsky_messages_published_total{tenant_id=\"$tid\"}")"
secs=$(awk -v a="$t1" -v b="$t0" 'BEGIN{printf "%.2f", a-b}')
rate=$(awk -v n="$ok_count" -v s="$secs" 'BEGIN{ if(s>0) printf "%.0f", n/s; else print "n/a" }')
delta=$(awk -v a="$after" -v b="$before" 'BEGIN{printf "%d", a-b}')

echo
ok "file→db: uploaded $ok_count/$COUNT files in ${secs}s (~${rate} files/s); published delta=$delta"
log "note: serial curl uploads bound this at the client, not the connector — see docs/LOAD.md."
stop_pipeline "$tid" "$cid"
