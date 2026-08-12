#!/usr/bin/env bash
# rotate-secrets.sh — replace the DEV secret.example values with real, strong,
# randomly-generated secrets. Run this yourself so the secrets are generated on
# your machine and never leave it.
#
# What it does (in place, preserving the DB schema):
#   * generates a random DB password, a fresh 32-byte ENCRYPTION_KEY (64 hex),
#     and new MinIO access/secret keys (via openssl)
#   * refuses to run if the `secrets` table is non-empty (rotating the master
#     key would orphan already-encrypted tenant credentials)
#   * ALTER ROLEs the live Postgres password (data preserved)
#   * writes the real values into the git-ignored secret.yaml files and applies
#     them, then restarts management-api + MinIO to pick them up
#
# The real values live ONLY in:
#   - the three infrastructure/kubernetes/**/secret.yaml files (git-ignored), and
#   - the corresponding Kubernetes Secrets in the cluster.
# BACK UP the ENCRYPTION_KEY somewhere safe (a password manager / Azure Key
# Vault): if it is lost, every secret encrypted with it becomes unrecoverable.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

PG_NS=vrsky-database
PG_POD=postgresql-0
PG_HOST=postgresql.vrsky-database.svc.cluster.local

# --- generate strong values (URL-safe) --------------------------------------
DB_PW="$(openssl rand -hex 16)"          # 32 hex chars
ENC_KEY="$(openssl rand -hex 32)"        # 64 hex chars = 32 bytes (AES-256)
MINIO_AK="vrsky$(openssl rand -hex 8)"   # 21 chars
MINIO_SK="$(openssl rand -hex 24)"       # 48 chars
CONN="postgresql://vrsky:${DB_PW}@${PG_HOST}:5432/vrsky?sslmode=disable"

# --- safety: never rotate the master key while encrypted data exists --------
CNT="$(kubectl exec -n "$PG_NS" "$PG_POD" -- psql -U vrsky -d vrsky -tAc 'SELECT count(*) FROM secrets;' | tr -d '[:space:]')"
if [ "$CNT" != "0" ]; then
  echo "ABORT: secrets table has $CNT row(s); rotating ENCRYPTION_KEY would orphan them." >&2
  echo "Re-key those secrets first, or rotate only the DB/MinIO creds by hand." >&2
  exit 1
fi

echo ">>> rotating live Postgres role password"
kubectl exec -n "$PG_NS" "$PG_POD" -- psql -U vrsky -d vrsky -c "ALTER ROLE vrsky WITH PASSWORD '${DB_PW}';"

echo ">>> writing git-ignored secret.yaml files"
cat > infrastructure/kubernetes/postgresql/secret.yaml <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: postgres-credentials
  namespace: vrsky-database
type: Opaque
stringData:
  vrsky-username: vrsky
  vrsky-password: ${DB_PW}
  connection_string: ${CONN}
EOF
cat > infrastructure/kubernetes/management-api/secret.yaml <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: postgres-credentials
  namespace: vrsky-platform
type: Opaque
stringData:
  connection_string: ${CONN}
  encryption_key: ${ENC_KEY}
EOF
cat > infrastructure/kubernetes/minio/secret.yaml <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: vrsky-storage
type: Opaque
stringData:
  accesskey: ${MINIO_AK}
  secretkey: ${MINIO_SK}
EOF
chmod 600 infrastructure/kubernetes/postgresql/secret.yaml \
          infrastructure/kubernetes/management-api/secret.yaml \
          infrastructure/kubernetes/minio/secret.yaml

echo ">>> applying secrets"
kubectl apply -f infrastructure/kubernetes/postgresql/secret.yaml
kubectl apply -f infrastructure/kubernetes/management-api/secret.yaml
kubectl apply -f infrastructure/kubernetes/minio/secret.yaml

echo ">>> restarting consumers"
kubectl rollout restart deploy/vrsky-management-api -n vrsky-platform
kubectl rollout restart deploy/minio -n vrsky-storage
kubectl rollout status  deploy/vrsky-management-api -n vrsky-platform --timeout=180s
kubectl rollout status  deploy/minio -n vrsky-storage --timeout=180s

echo ""
echo "Rotation complete. Verify:"
echo "  kubectl port-forward -n vrsky-platform svc/vrsky-management-api 18080:8080 & sleep 3; curl -s localhost:18080/readyz; kill %1"
echo ""
echo "IMPORTANT: back up the ENCRYPTION_KEY from infrastructure/kubernetes/management-api/secret.yaml"
echo "somewhere safe. Losing it makes every encrypted secret unrecoverable."
