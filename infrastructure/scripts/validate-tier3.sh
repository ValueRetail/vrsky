#!/usr/bin/env bash
# validate-tier3.sh — exercise the Tier-3 scalability paths on a live Kubernetes
# cluster to close epic #140 (service discovery #21, NATS autoscaling #19, and
# the HA/autoscale checks for #135/#136/#137/#138).
#
# This wraps the happy path of docs/TIER3_VALIDATION.md into pass/fail checks.
# It REQUIRES a reachable cluster (kubectl current-context) — it cannot run in a
# compose-only environment. Throughput re-measurement (DoD #4) is run separately
# via tests/load/run.sh because it needs a load target.
#
# Usage:
#   infrastructure/scripts/validate-tier3.sh <tenant-slug>
#
# Env overrides:
#   PLATFORM_NS   (default vrsky-platform)
#   TENANT_NS     (default vrsky-tenants)
#   MGMT_API_SVC  (default svc/vrsky-management-api:8080)
set -euo pipefail

TENANT="${1:-}"
PLATFORM_NS="${PLATFORM_NS:-vrsky-platform}"
TENANT_NS="${TENANT_NS:-vrsky-tenants}"
DB_NS="${DB_NS:-vrsky-database}"
STORAGE_NS="${STORAGE_NS:-vrsky-storage}"

RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YEL=$'\033[1;33m'; NC=$'\033[0m'
pass=0; fail=0
ok()   { echo -e "${GRN}✓ PASS${NC} $1"; pass=$((pass+1)); }
bad()  { echo -e "${RED}✗ FAIL${NC} $1"; fail=$((fail+1)); }
warn() { echo -e "${YEL}⚠${NC} $1"; }
hdr()  { echo; echo "=== $1 ==="; }

if [ -z "$TENANT" ]; then
  echo "usage: $0 <tenant-slug>" >&2; exit 2
fi
if ! kubectl cluster-info >/dev/null 2>&1; then
  echo "${RED}No reachable cluster — kubectl cluster-info failed. This script needs a live K8s cluster.${NC}" >&2
  exit 2
fi

# ---- #138 management-api HA -------------------------------------------------
hdr "#138 management-api runs >=2 replicas"
ready=$(kubectl get deploy vrsky-management-api -n "$PLATFORM_NS" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
if [ "${ready:-0}" -ge 2 ]; then ok "management-api readyReplicas=$ready (>=2)"; else bad "management-api readyReplicas=${ready:-0} (<2)"; fi
if kubectl get pdb vrsky-management-api -n "$PLATFORM_NS" >/dev/null 2>&1; then ok "management-api PodDisruptionBudget present"; else bad "management-api PDB missing"; fi

# ---- #137 PostgreSQL HA -----------------------------------------------------
hdr "#137 PostgreSQL HA (CloudNativePG)"
if kubectl get cluster.postgresql.cnpg.io vrsky-pg -n "$DB_NS" >/dev/null 2>&1; then
  insts=$(kubectl get cluster.postgresql.cnpg.io vrsky-pg -n "$DB_NS" -o jsonpath='{.status.instances}' 2>/dev/null || echo 0)
  if [ "${insts:-0}" -ge 3 ]; then ok "CNPG cluster has $insts instances (>=3)"; else bad "CNPG instances=${insts:-0} (<3)"; fi
else
  bad "CNPG cluster vrsky-pg not found (apply postgresql/cnpg-cluster.yaml)"
fi

# ---- #136 object storage HA -------------------------------------------------
hdr "#136 distributed MinIO"
mr=$(kubectl get statefulset minio -n "$STORAGE_NS" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
if [ "${mr:-0}" -ge 4 ]; then ok "MinIO StatefulSet readyReplicas=$mr (>=4, erasure-coded)"; else bad "MinIO readyReplicas=${mr:-0} (<4; apply minio/statefulset-distributed.yaml)"; fi

# ---- #135 worker autoscaling ------------------------------------------------
hdr "#135 per-connection worker HPA"
if kubectl get hpa -n "$PLATFORM_NS" 2>/dev/null | grep -q .; then
  ok "HPA objects present in $PLATFORM_NS (drive load to observe scaling)"
else
  warn "no HPA yet — deploy a connection and ensure metrics-server is installed, then re-check"
fi

# ---- #21 service discovery + health ----------------------------------------
hdr "#21 tenant NATS service discovery"
# Resolve a tenant id from slug via the management-api pod (psql) or skip with guidance.
TID=$(kubectl exec -n "$DB_NS" -i deploy/vrsky-pg-rw 2>/dev/null -- \
  psql -tAU vrsky -d vrsky -c "SELECT id FROM tenants WHERE slug='$TENANT' LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)
if [ -z "$TID" ]; then
  warn "could not resolve tenant id for slug '$TENANT' — provision it first: tenant-nats/provision-tenant-nats.sh $TENANT 1"
else
  # Hit the discovery endpoint from inside the cluster.
  body=$(kubectl run vrsky-disc-check --rm -i --restart=Never -n "$PLATFORM_NS" --image=curlimages/curl:8.8.0 -- \
    -s "http://vrsky-management-api.$PLATFORM_NS.svc.cluster.local:8080/api/v1/tenants/$TID/nats-instances" 2>/dev/null || true)
  if echo "$body" | grep -q 'nats://'; then ok "discovery returns instance URL(s) for $TENANT"; else bad "discovery returned no URLs: $body"; fi
fi

# ---- #19 autoscaler present + metrics --------------------------------------
hdr "#19 autoscaler metrics exported"
metrics=$(kubectl run vrsky-metrics-check --rm -i --restart=Never -n "$PLATFORM_NS" --image=curlimages/curl:8.8.0 -- \
  -s "http://vrsky-management-api.$PLATFORM_NS.svc.cluster.local:9090/metrics" 2>/dev/null || true)
if echo "$metrics" | grep -q 'vrsky_nats_instance_capacity_pct'; then
  ok "autoscaler capacity gauge exported (drive load past a trigger to observe scale-up)"
else
  warn "capacity gauge not yet present — no tenant instances scraped yet"
fi

echo
echo "=== Tier-3 validation: ${GRN}$pass passed${NC}, ${RED}$fail failed${NC} ==="
echo "Load/throughput (DoD #4) is run separately:"
echo "  tests/load/run.sh --rate 20000 --duration 60s webhook-to-http   # record in docs/LOAD.md"
echo "See docs/TIER3_VALIDATION.md for the full close-out checklist (#140)."
[ "$fail" -eq 0 ]
