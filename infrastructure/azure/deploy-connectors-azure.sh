#!/usr/bin/env bash
# deploy-connectors-azure.sh — deploy the VRSky connectors as standing services.
#
# Every connector subscribes to vrsky.commands.*.connection.{start,stop} on NATS
# and loads per-connection config from the connections table, decrypting
# credentials with ENCRYPTION_KEY. They run in vrsky-platform so they inherit
# the acr-pull secret (default SA) and can read the postgres-credentials secret
# (connection_string + encryption_key). Connectors with an HTTP surface (inbound
# webhooks, uploads, SSE event streams for the UI) also get a Service.
#
# These services ARE the runtime: since #201 (transforms) and #205 (edges) the
# orchestrator deploys no per-connection worker pods at all, so a node kind that
# has no standing service here simply does not run. The set below must therefore
# cover every source/destination type the UI offers — see the two dropdowns in
# ui/src/components/Pipeline/PropertyEditor.tsx. Each connector claims a node by
# its config `type` (e.g. cloud_storage → cloud-storage-{consumer,producer}).
#
# Replica counts. The default is 1; a connector is scaled to 2 + a PDB only
# when it is a PURE JetStream pull-durable subscriber — no local state, no
# per-pod HTTP surface. Replicas of a pull durable share the work correctly
# (same reasoning as data-filter/data-converter, #201), so that set gets HA on
# the destination side for free. Everything else stays a singleton because:
#   consumers        drive ingestion themselves (directory watches, API polls,
#                    CDC cursors) — a second replica duplicates every poll;
#   HTTP producers   serve the UI a live event stream (SSE) held per pod, so a
#                    second replica would show the operator half the events.
# Both are liftable — leader election for the pollers, a shared event bus for
# the streams — but neither is in scope for #205.
set -euo pipefail

REG="${REG:-vrskyprodacr}"
ACR="${REG}.azurecr.io/vrsky"
NS=vrsky-platform
NATS="nats://nats-platform.vrsky-platform.svc.cluster.local:4222"
MGMT="http://vrsky-management-api.vrsky-platform.svc.cluster.local:8080"
# Shared RWX volume backing the file source/destination node types. Azure Files
# is the AKS storage class that supports ReadWriteMany, which file-consumer
# (watch) and file-producer (write) both need to see the same tree.
FILES_PVC="${FILES_PVC:-vrsky-files}"
FILES_CLASS="${FILES_CLASS:-azurefile-csi}"
FILES_SIZE="${FILES_SIZE:-50Gi}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"; cd "$REPO_ROOT"
OUT="infrastructure/kubernetes/connectors"; mkdir -p "$OUT"
MANIFEST="$OUT/connectors.yaml"

# Service table: "name role port" — role is consumer|producer, port is the
# connector's HTTP surface (WORKER_HTTP_PORT) or "-" when it has none. Ports
# match docker-compose.yml so TEST and prod stay comparable.
#
# Retail/ERP connectors (deployed since 2026-08-25).
RETAIL="
sitoo-consumer            consumer 9260
front-systems-consumer    consumer 9270
brightpearl-consumer      consumer 9280
business-central-consumer consumer 9310
visma-consumer            consumer 9320
sap-s4hana-consumer       consumer 9290
sitoo-producer            producer -
front-systems-producer    producer -
business-central-producer producer -
visma-producer            producer -
brightpearl-producer      producer -
sap-s4hana-producer       producer -
"

# Generic connectors: the source/destination types that are not tied to one
# vendor. Undeployed until #205 — a pipeline ending in any of them delivered
# nothing, because the orchestrator's per-connection producer was a no-op.
GENERIC="
api-consumer              consumer 9800
webhook-consumer          consumer 9100
file-consumer             consumer 9200
db-consumer               consumer 9300
cloud-storage-consumer    consumer 9240
sftp-consumer             consumer 9210
kafka-consumer            consumer 9220
rabbitmq-consumer         consumer 9230
salesforce-consumer       consumer 9250
tenant-consumer           consumer -
http-producer             producer 9400
db-producer               producer 9500
file-producer             producer 9900
cloud-storage-producer    producer -
sftp-producer             producer -
kafka-producer            producer -
rabbitmq-producer         producer -
salesforce-producer       producer -
"

# extra_env <name> — connector-specific env beyond the shared block.
extra_env() {
  case "$1" in
    # OAuth output/input (#97): resolve access tokens for auth_type=oauth nodes.
    # The service secret is optional so a cluster without OAuth still starts.
    api-consumer|salesforce-consumer|salesforce-producer|http-producer)
      cat <<EOF
        - name: MGMT_API_URL
          value: "$MGMT"
        - name: OAUTH_TOKEN_SERVICE_SECRET
          valueFrom:
            secretKeyRef:
              name: oauth-token-service
              key: secret
              optional: true
EOF
      ;;
    file-consumer)
      cat <<EOF
        - name: FILE_CONSUMER_BASE_DIR
          value: "/data/input"
EOF
      ;;
    file-producer)
      cat <<EOF
        - name: FILE_OUTPUT_DIR
          value: "/data/output"
        - name: FILE_PRODUCER_HTTP_PORT
          value: "9900"
EOF
      ;;
  esac
}

# volume_mounts / volumes — only the file connectors touch the shared share.
volume_mounts() {
  case "$1" in
    file-consumer|file-producer)
      cat <<EOF
        volumeMounts:
        - name: files
          mountPath: /data
EOF
      ;;
  esac
}

volumes() {
  case "$1" in
    file-consumer|file-producer)
      cat <<EOF
      volumes:
      - name: files
        persistentVolumeClaim:
          claimName: $FILES_PVC
EOF
      ;;
  esac
}

emit_pvc() {
  cat <<EOF
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $FILES_PVC
  namespace: $NS
spec:
  accessModes:
  - ReadWriteMany
  storageClassName: $FILES_CLASS
  resources:
    requests:
      storage: $FILES_SIZE
EOF
}

emit() {  # $1=name  $2=role  $3=port ("-" for none)
  local name="$1" role="$2" port="$3" replicas=1 scaled=""
  [ "$port" = "-" ] && port=""
  # Pure pull-durable subscriber (producer, no HTTP surface) → safe to scale.
  if [ "$role" = "producer" ] && [ -z "$port" ]; then
    replicas=2
    scaled=yes
  fi

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
    role: $role
spec:
  replicas: $replicas
  selector:
    matchLabels:
      app: vrsky-$name
  template:
    metadata:
      labels:
        app: vrsky-$name
        tier: connector
        role: $role
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
        # Claim-check spill store (#187/#201): consumers offload >256KiB
        # payloads; producers rehydrate/stream them. Prereq: minio-credentials
        # secret copied into this namespace from vrsky-storage.
        - name: PAYLOAD_STORE_PROVIDER
          value: "s3"
        - name: PAYLOAD_STORE_BUCKET
          value: "vrsky-objects"
        - name: PAYLOAD_STORE_ENDPOINT
          value: "http://minio.vrsky-storage.svc.cluster.local:9000"
        - name: PAYLOAD_STORE_REGION
          value: "us-east-1"
        - name: PAYLOAD_STORE_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: accesskey
        - name: PAYLOAD_STORE_SECRET_KEY
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: secretkey
EOF
  extra_env "$name"
  if [ -n "$port" ]; then
    cat <<EOF
        - name: WORKER_HTTP_PORT
          value: "$port"
        ports:
        - name: http
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
            # 512Mi, NOT 128Mi: a producer that does not implement the ADR 0001
            # streaming path buffers an offloaded payload up to the rehydrate
            # cap (PAYLOAD_REHYDRATE_MAX_BYTES, 128 MiB default). A 128Mi limit
            # OOM-kills the pod before the cap can reject anything.
            memory: 512Mi
EOF
  volume_mounts "$name"
  volumes "$name"

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
  - name: http
    port: $port
    targetPort: $port
EOF
  fi

  # Only the scaled-out producers need a disruption budget; a singleton has
  # nothing to keep available during a drain.
  if [ -n "$scaled" ]; then
    cat <<EOF
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: vrsky-$name
  namespace: $NS
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: vrsky-$name
EOF
  fi
}

ALL="$RETAIL$GENERIC"

{
  emit_pvc
  while read -r name role port; do
    [ -z "$name" ] && continue
    emit "$name" "$role" "$port"
  done <<< "$ALL"
} > "$MANIFEST"

# GENERATE_ONLY=1 renders the manifest without touching a cluster — used by the
# manifest test and handy for reviewing a diff before applying.
if [ -n "${GENERATE_ONLY:-}" ]; then
  echo ">>> generated $MANIFEST (GENERATE_ONLY set — not applying)"
  exit 0
fi

echo ">>> validating $MANIFEST"
kubectl apply --dry-run=client -f "$MANIFEST" >/dev/null
echo ">>> applying"
kubectl apply -f "$MANIFEST"

echo ">>> waiting for rollouts"
while read -r name role port; do
  [ -z "$name" ] && continue
  kubectl rollout status deploy/vrsky-"$name" -n $NS --timeout=180s
done <<< "$ALL"

echo ""
kubectl get pods -n $NS -l tier=connector -o wide
