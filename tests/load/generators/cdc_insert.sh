#!/usr/bin/env bash
# CDC load generator: bulk-INSERT rows into the source Postgres and measure how
# the postgres-consumer captures them (Change-Data-Capture throughput).
#
# The postgres-consumer is env-driven (POSTGRES_INPUT_* in docker-compose.yml)
# and continuously decodes the logical-replication stream for everything in its
# publication, so this generator just needs to (1) make sure the load table is
# in the publication, (2) snapshot the capture counters, (3) insert N rows, and
# (4) wait for capture to settle and report the deltas + wall time.
#
# Usage: cdc_insert.sh [--rows N | --rate R --duration S] [--table NAME]
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/load/lib.sh
source "$HERE/../lib.sh"

ROWS=20000
RATE=""; DURATION=""
TABLE="load_cdc"
while [ $# -gt 0 ]; do
  case "$1" in
    --rows) ROWS="$2"; shift 2;;
    --rate) RATE="$2"; shift 2;;
    --duration) DURATION="$2"; shift 2;;
    --table) TABLE="$2"; shift 2;;
    *) shift;;
  esac
done
# --rate/--duration overrides --rows (rows = rate * seconds).
if [ -n "$RATE" ] && [ -n "$DURATION" ]; then
  secs="${DURATION%s}"; secs="${secs%m}"; [ "${DURATION: -1}" = "m" ] && secs=$((secs*60))
  ROWS=$((RATE * secs))
fi

psql_src() { docker exec "$SRC_DB_CONTAINER" psql -U "$SRC_DB_USER" -d "$SRC_DB_NAME" -q "$@"; }

need curl; need jq; need docker
log "ensuring table $TABLE exists and is in publication vrsky_publication…"
psql_src -c "CREATE TABLE IF NOT EXISTS $TABLE (id BIGSERIAL PRIMARY KEY, name text, amount int, created_at timestamptz DEFAULT now());" >/dev/null
# FOR ALL TABLES publications already cover it; otherwise add it explicitly.
psql_src -c "ALTER PUBLICATION vrsky_publication ADD TABLE $TABLE;" >/dev/null 2>&1 || true

cap_q='postgres_consumer_changes_captured_total'
bat_q='postgres_consumer_batches_published_total'
cap_before="$(prom_value "sum($cap_q)")"
bat_before="$(prom_value "sum($bat_q)")"

log "inserting $ROWS rows into $TABLE…"
t0=$(date +%s.%N)
psql_src -c "INSERT INTO $TABLE (name, amount) SELECT 'evt-'||g, (g%1000) FROM generate_series(1,$ROWS) g;" >/dev/null
t_inserted=$(date +%s.%N)

# Wait for capture to settle. Two phases so we don't mistake "hasn't started
# yet" for "done": first wait for the captured counter to rise above baseline
# (capture began), then wait for it to stop moving (capture finished). The
# counters are scraped every 15s, so allow generous time.
log "waiting for postgres-consumer to capture…"
started=0; stable=0; last="$cap_before"; waited=0
for _ in $(seq 1 60); do
  sleep 2; waited=$((waited+2))
  now="$(prom_value "sum($cap_q)")"
  if [ "$started" -eq 0 ]; then
    awk -v a="$now" -v b="$cap_before" 'BEGIN{exit !(a>b)}' && { started=1; last="$now"; }
    # Don't hang forever if the counter never moves — fail fast after ~40s.
    [ "$waited" -ge 40 ] && break
    continue
  fi
  if awk -v a="$now" -v b="$last" 'BEGIN{exit !(a==b)}'; then
    stable=$((stable+1)); [ "$stable" -ge 2 ] && break
  else
    stable=0
  fi
  last="$now"
done
[ "$started" -eq 1 ] || warn "capture not observed via counter within timeout (rows were inserted; counter is batch-granular and scraped every 15s)"
t_end=$(date +%s.%N)

cap_after="$(prom_value "sum($cap_q)")"
bat_after="$(prom_value "sum($bat_q)")"
insert_s=$(awk -v a="$t_inserted" -v b="$t0" 'BEGIN{printf "%.2f", a-b}')
total_s=$(awk -v a="$t_end" -v b="$t0" 'BEGIN{printf "%.2f", a-b}')
ins_rate=$(awk -v n="$ROWS" -v s="$insert_s" 'BEGIN{ if(s>0) printf "%.0f", n/s; else print "n/a" }')

echo
ok "CDC: inserted $ROWS rows in ${insert_s}s (~${ins_rate} rows/s into source)"
log "captured counter delta: $(awk -v a="$cap_after" -v b="$cap_before" 'BEGIN{printf "%d", a-b}') | batches: $(awk -v a="$bat_after" -v b="$bat_before" 'BEGIN{printf "%d", a-b}') | settled after ${total_s}s"
log "note: changes_captured_total is batch-granular; treat capture as confirmed end-to-end rather than a per-row rate (see docs/LOAD.md)."
