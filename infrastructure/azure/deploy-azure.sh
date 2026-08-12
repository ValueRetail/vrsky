#!/usr/bin/env bash
# deploy-azure.sh — bring up the VRSky CORE platform on Azure AKS.
#
# Wraps infrastructure/kubernetes/deploy-vrsky-platform.sh with the Azure-
# specific adjustments, WITHOUT editing the shared manifests (all rewrites
# happen on a throwaway copy):
#   1. pull images from ACR            (ghcr.io / localhost:5000  ->  ACR)
#   2. use an AKS storage class        (local-path  ->  managed-csi)
#   3. wire an `acr-pull` imagePullSecret onto the vrsky-platform namespace so
#      every pod — incl. orchestrator-spawned workers — can pull from ACR
#   4. point the orchestrator at ACR for per-connection worker images
#
# FIRST bring-up = Path A: in-cluster Postgres/MinIO + the committed
# secret.example.yaml DEV credentials. Ingress is skipped, so nothing is
# exposed publicly and this is safe for validation. ROTATE to real secrets
# before onboarding any real connection (see the tail of this script).
#
# Usage:  infrastructure/azure/deploy-azure.sh
#         REG=vrskyprodacr STORAGE_CLASS=managed-csi infrastructure/azure/deploy-azure.sh
set -euo pipefail

REG="${REG:-vrskyprodacr}"
ACR_LOGIN="${REG}.azurecr.io"
WORKER_REGISTRY="${ACR_LOGIN}/vrsky"          # orchestrator: {registry}/vrsky-{nodeType}:latest
STORAGE_CLASS="${STORAGE_CLASS:-managed-csi}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

echo "Registry      : $ACR_LOGIN"
echo "Storage class : $STORAGE_CLASS"
echo "Cluster       : $(kubectl config current-context)"

# --- 1. ACR pull credentials (fetched at runtime; never hardcoded) ----------
ACR_USER="$(az acr credential show -n "$REG" --query username -o tsv)"
ACR_PASS="$(az acr credential show -n "$REG" --query 'passwords[0].value' -o tsv)"

# --- 2. namespace + acr-pull secret + default-SA patch ----------------------
# vrsky-platform holds filter, management-api, and the orchestrator's workers.
# (Postgres/MinIO/NATS use public upstream images, so they need no pull secret.)
kubectl create namespace vrsky-platform --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret docker-registry acr-pull \
  --docker-server="$ACR_LOGIN" --docker-username="$ACR_USER" --docker-password="$ACR_PASS" \
  -n vrsky-platform --dry-run=client -o yaml | kubectl apply -f -
kubectl patch serviceaccount default -n vrsky-platform \
  -p '{"imagePullSecrets":[{"name":"acr-pull"}]}'

# --- 3. image + storage-class rewrite on a throwaway copy of the manifests ---
WORK="$(mktemp -d)/kubernetes"
mkdir -p "$WORK"
cp -R infrastructure/kubernetes/. "$WORK/"
# perl -pi (not BSD `sed -i`, which mis-parses multi-file in-place edits on macOS)
grep -rlE 'ghcr\.io/[Vv]alue[Rr]etail/vrsky/|localhost:5000/vrsky/|storageClassName:[[:space:]]*local-path' \
     "$WORK" --include='*.yaml' | while IFS= read -r f; do
  perl -pi -e '
    s{(?:ghcr\.io/[Vv]alue[Rr]etail/vrsky/|localhost:5000/vrsky/)}{'"$WORKER_REGISTRY"'/}g;
    s{(storageClassName:\s*)local-path}{${1}'"$STORAGE_CLASS"'}g;
  ' "$f"
done

# --- 3c. right-size app requests to fit small nodes (Standard_A2_v2, 2vCPU/4GB) ---
# The manifests are sized for big ServeTheWorld nodes; the filter alone asks for
# 1 CPU + 2Gi x3, which won't schedule here. Shrink it and drop replica counts.
# (management-api is already 100m/128Mi; just trim it to a single replica.)
perl -pi -e 's/^(\s*replicas:)\s*3\b/${1} 1/; s/cpu:\s*1000m/cpu: 100m/; s/memory:\s*2Gi/memory: 128Mi/; s/cpu:\s*2000m/cpu: 500m/; s/memory:\s*4Gi/memory: 256Mi/;' "$WORK/filter/deployment.yaml"
perl -pi -e 's/^(\s*replicas:)\s*2\b/${1} 1/;' "$WORK/management-api/deployment.yaml"

# management-api runs under its OWN service account (for orchestration RBAC),
# not the default one we patched — so give that SA the acr-pull secret too, or
# its pod pulls anonymously and gets 401 from ACR.
perl -0777 -pi -e 's/(kind: ServiceAccount\nmetadata:\n  name: vrsky-management-api\n  namespace: vrsky-platform)\n---/$1\nimagePullSecrets:\n- name: acr-pull\n---/' "$WORK/management-api/rbac.yaml"

# --- 3b. make the deploy script's readiness waits race-safe -----------------
# The script runs `kubectl wait --for=condition=ready pod -l ...` immediately
# after applying a statefulset/deployment; on AKS the pod object often doesn't
# exist yet, so `kubectl wait` errors ("no matching resources found") and the
# script aborts. Route those waits through a helper that first waits for the
# pod to appear. (Job waits are by-name and don't race, so they're untouched.)
DEPLOY="$WORK/deploy-vrsky-platform.sh"
perl -pi -e 's/\bkubectl wait --for=condition=ready pod -l\b/kubectl_wait_ready -l/g' "$DEPLOY"
HELPER='kubectl_wait_ready() { local sel ns to=300s; while [ $# -gt 0 ]; do case "$1" in -l) sel="$2"; shift 2;; -n) ns="$2"; shift 2;; --timeout=*) to="${1#--timeout=}"; shift;; *) shift;; esac; done; for _ in $(seq 1 60); do kubectl get pod -l "$sel" -n "$ns" 2>/dev/null | grep -q . && break; sleep 2; done; kubectl wait --for=condition=ready pod -l "$sel" -n "$ns" --timeout="$to"; }'
awk -v h="$HELPER" 'NR==1{print; print h; next} {print}' "$DEPLOY" > "$DEPLOY.tmp" && mv "$DEPLOY.tmp" "$DEPLOY"

# --- 4. run the platform deploy (core only) ---------------------------------
# Feeds the "Press Enter" prompt; monitoring/ingress are skipped via SKIP_*,
# and any later y/n prompt (demo tenant) gets 'n'.
echo ">>> deploying core: NATS -> Postgres -> MinIO -> filter -> management-api"
SKIP_MONITORING=true SKIP_INGRESS=true bash "$WORK/deploy-vrsky-platform.sh" <<< $'\nn\nn\nn'

# --- 5. point the orchestrator at ACR for per-connection worker images ------
kubectl set env deploy/vrsky-management-api -n vrsky-platform \
  WORKER_IMAGE_REGISTRY="$WORKER_REGISTRY" WORKER_IMAGE_VERSION=latest

cat <<EOF

Core platform deployed. Verify:
  kubectl get pods -A | grep vrsky
  kubectl port-forward -n vrsky-platform svc/vrsky-management-api 8080:8080 & curl -s localhost:8080/healthz

NEXT (not done by this script):
  * real secrets  — replace the DEV secret.example values (ENCRYPTION_KEY, DB
    password, MinIO creds) with real ones before any real connection.
  * UI            — deploy infrastructure/kubernetes/ui/ (add acr-pull to the
    vrsky-ui namespace the same way).
  * ingress + TLS — cert-manager + Ingress + a DNS record -> the LB IP, so the
    UI/API and the retail webhooks are reachable.
EOF
