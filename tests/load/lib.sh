#!/usr/bin/env bash
# Shared helpers for the VRSky load-test harness (tests/load/run.sh + the
# generators/). Pure bash + curl + jq + docker — no Go/npm dependency, and k6
# itself runs from the grafana/k6 container, so the only host tools needed are
# curl, jq and docker.
#
# Everything is overridable via env so the same harness runs against the local
# docker-compose stack (defaults below) or a remote one in CI.

# --- endpoints (host-published ports from docker-compose.yml) ---
MGMT_API="${MGMT_API:-http://localhost:3000}"        # management-api
PROM_API="${PROM_API:-http://localhost:9099}"        # prometheus (host 9099 -> 9090)
WEBHOOK_INGRESS="${WEBHOOK_INGRESS:-http://localhost:9100}" # webhook-consumer aux
FILE_INGRESS="${FILE_INGRESS:-http://localhost:9200}"      # file-consumer aux

# --- where k6 runs (on the compose network, talking to container names) ---
DOCKER_NETWORK="${DOCKER_NETWORK:-vrsky-network}"
K6_IMAGE="${K6_IMAGE:-grafana/k6:latest}"
# Container-internal URL k6 uses to reach the webhook ingress.
WEBHOOK_INGRESS_INTERNAL="${WEBHOOK_INGRESS_INTERNAL:-http://webhook-consumer:9100}"

# --- load-test account ---
# Auto-provisioned on first run. The minimal load stack has no mail server, so
# bootstrap_auth flips email_verified directly in the management DB rather than
# round-tripping a verification email. (Local/CI dev only — never on a real DB.)
LOAD_EMAIL="${LOAD_EMAIL:-loadtest@vrsky.local}"
LOAD_PASSWORD="${LOAD_PASSWORD:-LoadTest12345!}"
LOAD_FULLNAME="${LOAD_FULLNAME:-Load Tester}"
MGMT_DB_CONTAINER="${MGMT_DB_CONTAINER:-vrsky-postgres-management}"
MGMT_DB_USER="${MGMT_DB_USER:-postgres}"
MGMT_DB_NAME="${MGMT_DB_NAME:-management_db}"

# Source Postgres (CDC scenario)
SRC_DB_CONTAINER="${SRC_DB_CONTAINER:-vrsky-postgres-source}"
SRC_DB_USER="${SRC_DB_USER:-postgres}"
SRC_DB_NAME="${SRC_DB_NAME:-source_db}"

COOKIE_JAR="${COOKIE_JAR:-/tmp/vrsky_load_cookies.txt}"

# --- logging ---
if [ -t 1 ]; then
  RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YLW=$'\033[1;33m'; BLU=$'\033[0;34m'; NC=$'\033[0m'
else
  RED=; GRN=; YLW=; BLU=; NC=
fi
log()  { echo "${BLU}[load]${NC} $*"; }
ok()   { echo "${GRN}[ ok ]${NC} $*"; }
warn() { echo "${YLW}[warn]${NC} $*"; }
err()  { echo "${RED}[fail]${NC} $*" >&2; }

need() { command -v "$1" >/dev/null 2>&1 || { err "missing dependency: $1"; exit 1; }; }

# prom_value <promql> — prints the scalar value of the first result series, or 0.
prom_value() {
  curl -s --get "$PROM_API/api/v1/query" --data-urlencode "query=$1" \
    | jq -r '(.data.result[0].value[1]) // "0"' 2>/dev/null || echo 0
}

# bootstrap_auth — ensure the load-test user exists + is verified, log in,
# persist the session cookie to COOKIE_JAR, and echo the tenant id on stdout.
bootstrap_auth() {
  need curl; need jq
  rm -f "$COOKIE_JAR"
  # Register is best-effort: a duplicate email just 4xxs, which is fine on re-run.
  curl -s -X POST "$MGMT_API/api/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$LOAD_EMAIL\",\"password\":\"$LOAD_PASSWORD\",\"full_name\":\"$LOAD_FULLNAME\"}" >/dev/null 2>&1 || true

  # Try logging in first. This is all that's needed when the address is already
  # verified (re-runs, or a server that doesn't gate login on verification).
  local login
  login=$(_login)
  if ! echo "$login" | jq -e '.success == true' >/dev/null 2>&1; then
    # Login failed — most likely the address isn't verified and the load stack
    # has no mail server. As a best-effort *fallback only*, flip email_verified
    # directly in the management DB (needs docker access to that container) and
    # retry. Doing this only on failure keeps the harness usable against a
    # reachable MGMT_API whose DB container isn't local. (Local/CI dev only.)
    if command -v docker >/dev/null 2>&1; then
      docker exec "$MGMT_DB_CONTAINER" psql -U "$MGMT_DB_USER" -d "$MGMT_DB_NAME" -q \
        -c "UPDATE users SET email_verified=true, email_verified_at=now(), status='active' WHERE email='$LOAD_EMAIL';" >/dev/null 2>&1 || true
      login=$(_login)
    fi
  fi
  echo "$login" | jq -e '.success == true' >/dev/null 2>&1 || { err "login failed: $login"; return 1; }

  local tid
  tid=$(curl -s -b "$COOKIE_JAR" "$MGMT_API/api/v1/tenants" | jq -r '.tenants[0].id // empty')
  [ -n "$tid" ] || { err "no tenant found for load-test user $LOAD_EMAIL"; return 1; }
  echo "$tid"
}

# _login posts the load-test credentials and echoes the raw login response,
# persisting the session cookie to COOKIE_JAR.
_login() {
  curl -s -c "$COOKIE_JAR" -X POST "$MGMT_API/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$LOAD_EMAIL\",\"password\":\"$LOAD_PASSWORD\"}"
}

# deploy_pipeline <tenant_id> <name> <nodes_json> <edges_json> — create + start a
# connection, echo its id. Caller owns stopping it (stop_pipeline).
deploy_pipeline() {
  local tid="$1" name="$2" nodes="$3" edges="$4" body resp cid
  body=$(jq -n --arg t "$tid" --arg n "$name" --argjson nodes "$nodes" --argjson edges "$edges" \
    '{tenant_id:$t, name:$n, nodes:$nodes, edges:$edges}')
  resp=$(curl -s -b "$COOKIE_JAR" -X POST "$MGMT_API/api/v1/connections" \
    -H 'Content-Type: application/json' -H "X-Tenant-ID: $tid" -d "$body")
  cid=$(echo "$resp" | jq -r '.data.id // .id // empty')
  [ -n "$cid" ] || { err "create connection failed: $resp"; return 1; }
  curl -s -b "$COOKIE_JAR" -X POST "$MGMT_API/api/v1/connections/$cid/start" \
    -H "X-Tenant-ID: $tid" -o /dev/null
  echo "$cid"
}

# stop_pipeline <tenant_id> <conn_id>
stop_pipeline() {
  curl -s -b "$COOKIE_JAR" -X POST "$MGMT_API/api/v1/connections/$2/stop" \
    -H "X-Tenant-ID: $1" -o /dev/null 2>&1 || true
}

# result_row <scenario> <p99_ms> <sustained_msgs> <delivered> <errors>
result_row() {
  printf '%s  %-16s p99=%-9s sustained=%-11s delivered=%-8s errors=%s%s\n' \
    "$GRN" "$1" "${2}ms" "${3}/s" "$4" "$5" "$NC"
}
