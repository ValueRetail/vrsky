#!/usr/bin/env bash
# deploy-connectors-azure.sh — deploy the retail connectors as standing services.
#
# Each connector subscribes to vrsky.commands.*.connection.{start,stop} on NATS
# and loads per-connection config from the connections table, decrypting
# credentials with ENCRYPTION_KEY. They run in vrsky-platform so they inherit
# the acr-pull secret (default SA) and can read the postgres-credentials secret
# (connection_string + encryption_key). Webhook consumers also get a Service.
#
# 1 replica each (singleton — avoids duplicate per-connection pollers).
set -euo pipefail

REG="${REG:-vrskyprodacr}"
ACR="${REG}.azurecr.io/vrsky"
NS=vrsky-platform
NATS="nats://nats-platform.vrsky-platform.svc.cluster.local:4222"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"; cd "$REPO_ROOT"
OUT="infrastructure/kubernetes/connectors"; mkdir -p "$OUT"
MANIFEST="$OUT/connectors.yaml"

# webhook consumers "name:port" (serve inbound webhooks); everything else is poll/outbound-only
WEBHOOK="sitoo-consumer:9260 front-systems-consumer:9270 brightpearl-consumer:9280"
POLL="business-central-consumer visma-consumer sitoo-producer front-systems-producer business-central-producer visma-producer brightpearl-producer"

emit() {  # $1=name  $2=port(optional)
  local name="$1" port="${2:-}"
  cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vrsky-$name
  namespace: $NS
  labels:
    app: vrsky-$name
    tier: connector
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vrsky-$name
  template:
    metadata:
      labels:
        app: vrsky-$name
        tier: connector
    spec:
      containers:
      - name: $name
        image: $ACR/$name:latest
        env:
        - name: NATS_URL
          value: "$NATS"
        - name: LOG_LEVEL
          value: "info"
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: postgres-credentials
              key: connection_string
        - name: ENCRYPTION_KEY
          valueFrom:
            secretKeyRef:
              name: postgres-credentials
              key: encryption_key
EOF
  if [ -n "$port" ]; then
    cat <<EOF
        - name: WORKER_HTTP_PORT
          value: "$port"
        ports:
        - name: webhook
          containerPort: $port
EOF
  fi
  cat <<EOF
        resources:
          requests:
            cpu: 25m
            memory: 64Mi
          limits:
            cpu: 250m
            memory: 128Mi
EOF
  if [ -n "$port" ]; then
    cat <<EOF
---
apiVersion: v1
kind: Service
metadata:
  name: vrsky-$name
  namespace: $NS
  labels:
    app: vrsky-$name
spec:
  selector:
    app: vrsky-$name
  ports:
  - name: webhook
    port: $port
    targetPort: $port
EOF
  fi
}

{
  for w in $WEBHOOK; do emit "${w%%:*}" "${w##*:}"; done
  for p in $POLL;    do emit "$p"; done
} > "$MANIFEST"

echo ">>> validating $MANIFEST"
kubectl apply --dry-run=client -f "$MANIFEST" >/dev/null
echo ">>> applying"
kubectl apply -f "$MANIFEST"

echo ">>> waiting for rollouts"
for w in $WEBHOOK; do kubectl rollout status deploy/vrsky-${w%%:*} -n $NS --timeout=120s; done
for p in $POLL;    do kubectl rollout status deploy/vrsky-$p -n $NS --timeout=120s; done

echo ""
kubectl get pods -n $NS -l tier=connector -o wide
