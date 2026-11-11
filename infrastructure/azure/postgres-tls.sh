#!/usr/bin/env bash
# postgres-tls.sh — TLS between the services and the in-cluster Postgres (#453).
#
# Run by a human against production, one step at a time, in the order in
# plans/issue-453-postgres-tls.md. The deploy job never runs it. Nothing here
# prints a connection string or any other secret: connection strings are read
# into shell variables, changed there, and written back through stdin.
#
#   check              read-only: what is on, what is missing, who still talks
#                      to Postgres without TLS
#   server             apply tls.yaml and statefulset.yaml: Postgres gets a
#                      certificate, ssl = on and its pg_hba.conf from the
#                      ConfigMap postgres-hba (#729), which refuses plain
#                      connections from other pods. Restarts Postgres: a
#                      minute or more with no database (the disk detaches and
#                      re-attaches).
#   ca-sync            copy the PUBLIC ca.crt into the ConfigMap postgres-ca in
#                      vrsky-platform and vrsky-database. Run again whenever the
#                      CA certificate is renewed (every nine years).
#   clients            add the postgres-ca volume to the three core Deployments
#                      the deploy job only updates by image (management-api,
#                      data-filter, data-converter). The connectors and the
#                      backup CronJob get theirs from the deploy job.
#   urls <mode>        set sslmode in the connection strings of the Secrets
#                      postgres-credentials (vrsky-platform and vrsky-database)
#                      and vrsky-backup-config (BACKUP_DB_URL). Mode is
#                      verify-full (with sslrootcert), require (TLS without
#                      checking the server: the rollback) or disable (no TLS;
#                      refused while Postgres turns plain connections away).
#                      Running pods keep the old value until `restart`.
#   canary             run one throwaway pod in vrsky-platform that connects with
#                      sslmode=verify-full and the mounted CA, using the live
#                      Secret's host, user and password, and prints whether the
#                      session used TLS. Do this before `urls verify-full`.
#   restart            roll the Deployments that read DATABASE_URL, so they pick
#                      up the Secret.
#
# Every subcommand except `check` writes to the cluster. Each prints the kube
# context first and refuses to run unless CONFIRM=1 is set, so a command pasted
# into the wrong terminal does nothing. DRY_RUN=1 prints what a step would do,
# changes nothing and needs no CONFIRM; `check` only reads.
set -euo pipefail
set +x

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DB_NS=vrsky-database
PLATFORM_NS=vrsky-platform
PG_POD=postgresql-0
TLS_SECRET=postgresql-tls
CA_CONFIGMAP=postgres-ca
# Where every client pod mounts the CA (the postgres-ca volume).
CA_PATH=/etc/vrsky/postgres-ca/ca.crt
STORAGE_CLASS="${STORAGE_CLASS:-managed-csi}"
DRY_RUN="${DRY_RUN:-}"

CORE_DEPLOYS="vrsky-management-api vrsky-data-filter vrsky-data-converter"

die() { echo "ERROR: $*" >&2; exit 1; }
say() { echo ">>> $*"; }

# write_gate — the first thing every writing subcommand calls, before any
# kubectl call that could change something. Prints the context it would use.
write_gate() {
  command -v kubectl >/dev/null || die "kubectl not found"
  say "kube context: $(kubectl config current-context 2>/dev/null || echo unknown)"
  if [ -n "$DRY_RUN" ]; then return 0; fi
  [ "${CONFIRM:-}" = 1 ] || die "this step changes the cluster named above; check the context, then run it again with CONFIRM=1 (DRY_RUN=1 shows what it would do)"
}

# The connector Deployments (label tier=connector) in vrsky-platform; the
# generated manifest has 33. Fewer means a half-listed fleet, and a step that
# trusts it would switch or restart only part of it.
EXPECTED_CONNECTORS="${EXPECTED_CONNECTORS:-33}"
CONNECTORS=""

# load_connectors — fills CONNECTORS or dies. Call it directly, never inside
# $(...): a die in a subshell would not stop the script.
load_connectors() {
  local out n
  out="$(kubectl get deploy -n "$PLATFORM_NS" -l tier=connector -o name)" \
    || die "could not list the connector Deployments in $PLATFORM_NS"
  CONNECTORS="$(printf '%s\n' "$out" | sed 's#.*/##' | sed '/^$/d')"
  n="$(printf '%s\n' "$CONNECTORS" | sed '/^$/d' | wc -l | tr -d '[:space:]')"
  [ "$n" -ge "$EXPECTED_CONNECTORS" ] \
    || die "found $n connector Deployments in $PLATFORM_NS, expected at least $EXPECTED_CONNECTORS (set EXPECTED_CONNECTORS if the fleet really shrank)"
}

# act <description> <command…> — run a mutating command unless DRY_RUN is set.
act() {
  local what="$1"; shift
  if [ -n "$DRY_RUN" ]; then echo "    would: $what"; return 0; fi
  "$@"
}

# retarget_url <url> <verify-full|require|disable> — the URL with its sslmode (and
# sslrootcert) replaced; every other query parameter stays. Prints the result
# on stdout, so call it only into a variable.
retarget_url() {
  local url="$1" mode="$2" base query="" part out=""
  base="${url%%\?*}"
  if [ "$base" != "$url" ]; then query="${url#*\?}"; fi
  local oldifs="$IFS"; IFS='&'
  set -f
  # shellcheck disable=SC2086
  set -- $query
  set +f
  IFS="$oldifs"
  for part in "$@"; do
    case "$part" in
      sslmode=* | sslrootcert=* | "") ;;
      *) out="${out}${part}&" ;;
    esac
  done
  case "$mode" in
    verify-full) out="${out}sslmode=verify-full&sslrootcert=${CA_PATH}" ;;
    # No sslrootcert: with one, libpq and lib/pq treat require as verify-ca.
    require) out="${out}sslmode=require" ;;
    disable) out="${out}sslmode=disable" ;;
    *) die "unknown mode '$mode' (verify-full, require or disable)" ;;
  esac
  printf '%s?%s' "$base" "$out"
}

# url_sslmode <url> — the sslmode value, or "none". Safe to print.
url_sslmode() {
  local m
  m="$(printf '%s' "$1" | sed -n 's/.*[?&]sslmode=\([A-Za-z-]*\).*/\1/p')"
  printf '%s' "${m:-none}"
}

secret_value() { # ns secret key — empty when the Secret or key is missing
  kubectl get secret "$2" -n "$1" -o "jsonpath={.data.$3}" 2>/dev/null | base64 -d 2>/dev/null || true
}

# set_conn <ns> <secret> <key> <mode> — change sslmode inside one Secret value.
set_conn() {
  local ns="$1" name="$2" key="$3" mode="$4" cur new b64
  cur="$(secret_value "$ns" "$name" "$key")"
  if [ -z "$cur" ]; then
    echo "    $ns/$name has no $key — skipped"
    return 0
  fi
  new="$(retarget_url "$cur" "$mode")"
  if [ "$new" = "$cur" ]; then
    echo "    $ns/$name $key already $mode"
    return 0
  fi
  if [ -n "$DRY_RUN" ]; then
    echo "    would: set sslmode=$mode in $ns/$name $key (now $(url_sslmode "$cur"))"
    return 0
  fi
  b64="$(printf '%s' "$new" | base64 | tr -d '\n')"
  printf '{"data":{"%s":"%s"}}' "$key" "$b64" \
    | kubectl patch secret "$name" -n "$ns" --type merge --patch-file /dev/stdin >/dev/null
  echo "    $ns/$name $key: sslmode $(url_sslmode "$cur") -> $mode"
}

ca_from_secret() { # prints the CA certificate (public) from the server Secret
  kubectl get secret "$TLS_SECRET" -n "$DB_NS" -o 'jsonpath={.data.ca\.crt}' | base64 -d
}

# cert_fingerprint: SHA-256 fingerprint of the first certificate on stdin.
# Comparing fingerprints, not raw bytes: a certificate read through $(...)
# loses its trailing newline, so a byte hash never matched the file.
cert_fingerprint() { openssl x509 -noout -fingerprint -sha256 2>/dev/null; }

# names_have_ca <volume names, space separated> — is postgres-ca among them. The
# names go through a variable, not a pipe: `grep -q` quits early, and with
# pipefail the writer's SIGPIPE would turn a match into a failure.
names_have_ca() {
  case " $1 " in *" $CA_CONFIGMAP "*) return 0 ;; esac
  return 1
}

has_ca_volume() { # <kubectl get args…> — does the pod template carry postgres-ca
  local names
  names="$(kubectl get "$@" -o 'jsonpath={.spec.template.spec.volumes[*].name}' 2>/dev/null || true)"
  names_have_ca "$names"
}

cronjob_has_ca() {
  local names
  names="$(kubectl get cronjob vrsky-mgmt-backup -n "$DB_NS" -o 'jsonpath={.spec.jobTemplate.spec.template.spec.volumes[*].name}' 2>/dev/null || true)"
  names_have_ca "$names"
}

pg_sql() { kubectl exec -n "$DB_NS" "$PG_POD" -c postgresql -- psql -U vrsky -d vrsky -tAc "$1"; }

# The pg_hba.conf rules that may let a client from another pod in without
# TLS. 0 means Postgres refuses plain connections. Not counted: local (the
# socket), hostssl, hostgssenc (encrypted), and host rules for exactly one
# loopback address (127.0.0.1/32, ::1/128; the pod's own probes, kubectl exec,
# port-forward). Counted: every other host, hostnossl or hostnogssenc rule,
# including a loopback address with a wider mask (127.0.0.1/0 is everyone),
# and any line Postgres could not parse. A narrow rule such as
# `host … 10.1.2.3/32` counts too, so `urls disable` refuses on it. That is
# deliberate: one address taking plain connections does not make it safe to
# move every client to sslmode=disable.
PLAIN_RULES_SQL="SELECT count(*) FROM pg_hba_file_rules WHERE (type NOT IN ('local', 'hostssl', 'hostgssenc') AND NOT (coalesce(address, '') IN ('127.0.0.1', '::1') AND coalesce(netmask, '') IN ('255.255.255.255', 'ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'))) OR error IS NOT NULL"

# Every rule as Postgres parsed it, for a human to read before and after
# `server`. Addresses and auth methods only; nothing secret.
HBA_RULES_SQL="SELECT line_number, type, array_to_string(database, ','), array_to_string(user_name, ','), coalesce(address, ''), coalesce(netmask, ''), auth_method, coalesce(error, '') FROM pg_hba_file_rules ORDER BY line_number"

cmd_server() {
  write_gate
  kubectl get crd certificates.cert-manager.io >/dev/null 2>&1 \
    || die "cert-manager is not installed (infrastructure/kubernetes/ingress/install-cert-manager.sh)"
  local pg="$REPO_ROOT/infrastructure/kubernetes/postgresql"
  say "certificates (tls.yaml)"
  act "kubectl apply -f tls.yaml" kubectl apply -f "$pg/tls.yaml"
  if [ -z "$DRY_RUN" ]; then
    kubectl wait --for=condition=Ready certificate/postgresql-ca certificate/postgresql-tls \
      -n "$DB_NS" --timeout=120s
  fi
  say "StatefulSet (storage class $STORAGE_CLASS, as at the first bring-up)"
  local rendered
  rendered="$(perl -pe 's/(storageClassName:\s*)local-path/${1}'"$STORAGE_CLASS"'/' "$pg/statefulset.yaml")"
  # kubectl diff exits 1 when there is a difference; that is the point.
  printf '%s\n' "$rendered" | kubectl diff -f - || true
  if [ -n "$DRY_RUN" ]; then
    echo "    would: kubectl apply the StatefulSet above (restarts postgresql-0)"
    return 0
  fi
  printf '%s\n' "$rendered" | kubectl apply -f -
  # The disk detaches from the old pod and attaches to the new one; that can
  # take longer than a minute. If it does not come up: kubectl rollout undo
  # statefulset/postgresql, and kubectl delete pod postgresql-0 (a rolling
  # update does not replace a pod that never became Ready).
  kubectl rollout status statefulset/postgresql -n "$DB_NS" --timeout=600s
  say "ssl is $(pg_sql 'SHOW ssl')"
  say "hba_file is $(pg_sql 'SHOW hba_file')"
  say "pg_hba.conf rules that let another pod in without TLS: $(pg_sql "$PLAIN_RULES_SQL") (want 0)"
}

cmd_ca_sync() {
  write_gate
  command -v openssl >/dev/null || die "openssl not found"
  # No temporary file and no trap: the CA is public, so it can sit in a
  # variable and go to kubectl as a literal. (A RETURN trap fires again when
  # main returns on bash 3.2, the macOS default, and aborts the script.)
  local ca ns
  ca="$(ca_from_secret)" || die "could not read ca.crt from $DB_NS/$TLS_SECRET"
  [ -n "$ca" ] || die "no ca.crt in $DB_NS/$TLS_SECRET (is the Certificate Ready?)"
  printf '%s\n' "$ca" | openssl x509 -noout >/dev/null 2>&1 || die "ca.crt is not a certificate"
  say "CA $(printf '%s\n' "$ca" | openssl x509 -noout -subject -enddate | tr '\n' ' ')"
  for ns in "$PLATFORM_NS" "$DB_NS"; do
    if [ -n "$DRY_RUN" ]; then echo "    would: ConfigMap $ns/$CA_CONFIGMAP <- ca.crt"; continue; fi
    kubectl get namespace "$ns" >/dev/null
    kubectl create configmap "$CA_CONFIGMAP" -n "$ns" --from-literal=ca.crt="$ca"$'\n' \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    echo "    $ns/$CA_CONFIGMAP updated"
  done
}

cmd_clients() {
  write_gate
  local d c patch
  for d in $CORE_DEPLOYS; do
    c="${d#vrsky-}"
    if has_ca_volume deploy "$d" -n "$PLATFORM_NS"; then
      echo "    $d already mounts $CA_CONFIGMAP"
      continue
    fi
    patch='{"spec":{"template":{"spec":{"volumes":[{"name":"'"$CA_CONFIGMAP"'","configMap":{"name":"'"$CA_CONFIGMAP"'","optional":true}}],"containers":[{"name":"'"$c"'","volumeMounts":[{"name":"'"$CA_CONFIGMAP"'","mountPath":"'"${CA_PATH%/*}"'","readOnly":true}]}]}}}}'
    act "mount $CA_CONFIGMAP in $d" kubectl patch deploy "$d" -n "$PLATFORM_NS" --type strategic -p "$patch"
  done
  if [ -z "$DRY_RUN" ]; then
    for d in $CORE_DEPLOYS; do kubectl rollout status deploy/"$d" -n "$PLATFORM_NS" --timeout=240s; done
  fi
}

# Every Deployment that reads the connection string must already mount the CA,
# or its next restart on verify-full cannot start a connection.
require_clients_ready() {
  local missing="" d
  load_connectors
  for d in $CORE_DEPLOYS $CONNECTORS; do
    has_ca_volume deploy "$d" -n "$PLATFORM_NS" || missing="$missing $d"
  done
  if kubectl get cronjob vrsky-mgmt-backup -n "$DB_NS" >/dev/null 2>&1 && ! cronjob_has_ca; then
    missing="$missing cronjob/vrsky-mgmt-backup"
  fi
  [ -z "$missing" ] || die "these do not mount $CA_CONFIGMAP yet:$missing (merge the PR so the deploy job rolls the connectors and the backup job; run 'clients' for the core services)"
}

cmd_urls() {
  local mode="${1:-}"
  case "$mode" in verify-full | require | disable) ;; *) die "usage: urls <verify-full|require|disable>" ;; esac
  write_gate
  if [ "$mode" = disable ]; then
    # Fail closed: against hostssl, every client restarted on sslmode=disable
    # would be turned away.
    local plain
    plain="$(pg_sql "$PLAIN_RULES_SQL" | tr -d '[:space:]')" || die "could not read pg_hba_file_rules"
    [ -n "$plain" ] && [ "$plain" != 0 ] \
      || die "Postgres refuses plain connections (pg_hba.conf from the ConfigMap postgres-hba, #729), so no client could connect on sslmode=disable. Use 'urls require' (TLS without checking the server), or allow plain connections first (infrastructure/kubernetes/postgresql/README.md, Rollback)"
  fi
  if [ "$mode" = require ]; then
    [ "$(pg_sql 'SHOW ssl' | tr -d '[:space:]')" = on ] || die "Postgres does not have ssl on (run 'server'); clients on sslmode=require could not connect"
  fi
  if [ "$mode" = verify-full ]; then
    # Fail closed: the strings must not point at a server or a CA that is not there.
    [ "$(pg_sql 'SHOW ssl' | tr -d '[:space:]')" = on ] || die "Postgres does not have ssl on yet (run 'server')"
    local ns
    for ns in "$PLATFORM_NS" "$DB_NS"; do
      [ -n "$(kubectl get configmap "$CA_CONFIGMAP" -n "$ns" -o 'jsonpath={.data.ca\.crt}' 2>/dev/null)" ] \
        || die "no $ns/$CA_CONFIGMAP with ca.crt (run 'ca-sync')"
    done
    require_clients_ready
  fi
  set_conn "$PLATFORM_NS" postgres-credentials connection_string "$mode"
  set_conn "$DB_NS" postgres-credentials connection_string "$mode"
  set_conn "$DB_NS" vrsky-backup-config BACKUP_DB_URL "$mode"
  say "running pods keep the old value until: $0 restart"
}

# One pod, one query, no change to anything that exists. It reads the live
# Secret into its own environment and swaps only the query part of the URL.
# The pod's output is psql's answer or psql's error, neither of which holds the
# password.
canary_cleanup() {
  kubectl delete pod pg-tls-canary -n "$PLATFORM_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}

cmd_canary() {
  write_gate
  local pod=pg-tls-canary
  if [ -n "$DRY_RUN" ]; then echo "    would: run pod $PLATFORM_NS/$pod and print its result"; return 0; fi
  # However this ends (success, failure, Ctrl-C, a signal), the pod goes.
  trap canary_cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  kubectl delete pod "$pod" -n "$PLATFORM_NS" --ignore-not-found >/dev/null
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $pod
  namespace: $PLATFORM_NS
  labels:
    app: $pod
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 70
    runAsGroup: 70
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: psql
      image: postgres:18-alpine
      command: ["sh", "-c"]
      args:
        - |
          psql "\${DATABASE_URL%%\?*}?sslmode=verify-full&sslrootcert=$CA_PATH" -tAc "SELECT 'ssl=' || ssl || ' ' || version FROM pg_stat_ssl WHERE pid = pg_backend_pid()"
      env:
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: postgres-credentials
              key: connection_string
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: ca
          mountPath: ${CA_PATH%/*}
          readOnly: true
  volumes:
    - name: ca
      configMap:
        name: $CA_CONFIGMAP
EOF
  local phase="" i
  for i in $(seq 1 45); do
    phase="$(kubectl get pod "$pod" -n "$PLATFORM_NS" -o 'jsonpath={.status.phase}' 2>/dev/null || true)"
    case "$phase" in Succeeded | Failed) break ;; esac
    sleep 2
  done
  echo "    pod phase: ${phase:-unknown}"
  kubectl logs "$pod" -n "$PLATFORM_NS" 2>&1 | sed 's/^/    /' || true
  canary_cleanup
  [ "$phase" = Succeeded ] || die "the canary did not connect over TLS; do not switch the strings"
}

cmd_restart() {
  write_gate
  local d all
  load_connectors
  all="$CORE_DEPLOYS $CONNECTORS"
  # One at a time: management-api first, so a bad string shows up on the one
  # service that has a readiness check on the database.
  for d in $all; do
    act "rollout restart $d" kubectl rollout restart deploy/"$d" -n "$PLATFORM_NS"
    if [ -z "$DRY_RUN" ]; then kubectl rollout status deploy/"$d" -n "$PLATFORM_NS" --timeout=240s; fi
  done
}

cmd_check() {
  command -v kubectl >/dev/null || die "kubectl not found"
  say "kube context: $(kubectl config current-context 2>/dev/null || echo unknown)"
  local bad=0

  echo "--- cert-manager and certificates"
  kubectl get crd certificates.cert-manager.io >/dev/null 2>&1 && echo "    cert-manager CRDs: present" || { echo "    cert-manager CRDs: MISSING"; bad=1; }
  local c
  for c in postgresql-ca postgresql-tls; do
    echo "    certificate/$c Ready: $(kubectl get certificate "$c" -n "$DB_NS" -o 'jsonpath={.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  done
  local crt
  crt="$(kubectl get secret "$TLS_SECRET" -n "$DB_NS" -o 'jsonpath={.data.tls\.crt}' 2>/dev/null | base64 -d 2>/dev/null || true)"
  if [ -n "$crt" ]; then
    echo "    server certificate: $(printf '%s' "$crt" | openssl x509 -noout -enddate)"
    local want have
    want="$(printf '%s\n' "$crt" | cert_fingerprint || true)"
    have="$(kubectl exec -n "$DB_NS" "$PG_POD" -c postgresql -- cat /etc/postgresql/tls/tls.crt 2>/dev/null | cert_fingerprint || true)"
    if [ -n "$want" ] && [ "$want" = "$have" ]; then echo "    Postgres serves the Secret's current certificate"; else echo "    Postgres has an OLDER certificate than the Secret: kubectl rollout restart statefulset/postgresql -n $DB_NS"; fi
  fi

  echo "--- server"
  echo "    ssl = $(pg_sql 'SHOW ssl' 2>/dev/null | tr -d '[:space:]')"
  echo "    hba_file = $(pg_sql 'SHOW hba_file' 2>/dev/null | tr -d '[:space:]')"
  echo "    pg_hba.conf rules that let another pod in without TLS: $(pg_sql "$PLAIN_RULES_SQL" 2>/dev/null | tr -d '[:space:]') (0: plain connections are refused)"
  echo "    pg_hba.conf as Postgres reads it (line|type|database|user|address|netmask|method|error):"
  pg_sql "$HBA_RULES_SQL" 2>/dev/null | sed 's/^/      /' || echo "      (could not query)"
  echo "    network client connections by ssl (a session on the unix socket, like this one, is not counted):"
  pg_sql "SELECT '      ssl=' || s.ssl || '  ' || count(*) FROM pg_stat_ssl s JOIN pg_stat_activity a USING (pid) WHERE a.backend_type = 'client backend' AND a.client_addr IS NOT NULL GROUP BY s.ssl ORDER BY s.ssl DESC" 2>/dev/null || echo "      (could not query)"
  echo "    client addresses WITHOUT ssl (match them to pods with: kubectl get pods -A -o wide):"
  pg_sql "SELECT '      ' || a.client_addr || '  x' || count(*) FROM pg_stat_ssl s JOIN pg_stat_activity a USING (pid) WHERE a.backend_type = 'client backend' AND NOT s.ssl AND a.client_addr IS NOT NULL GROUP BY a.client_addr ORDER BY 1" 2>/dev/null || true

  echo "--- CA copies"
  local ns ca_want ca_have
  ca_want="$(ca_from_secret 2>/dev/null || true)"
  for ns in "$PLATFORM_NS" "$DB_NS"; do
    ca_have="$(kubectl get configmap "$CA_CONFIGMAP" -n "$ns" -o 'jsonpath={.data.ca\.crt}' 2>/dev/null || true)"
    if [ -n "$ca_want" ] && [ "$ca_have" = "$ca_want" ]; then echo "    $ns/$CA_CONFIGMAP: matches the CA"; else echo "    $ns/$CA_CONFIGMAP: MISSING or different (run ca-sync)"; bad=1; fi
  done

  echo "--- connection strings (sslmode only)"
  echo "    $PLATFORM_NS/postgres-credentials: $(url_sslmode "$(secret_value $PLATFORM_NS postgres-credentials connection_string)")"
  echo "    $DB_NS/postgres-credentials: $(url_sslmode "$(secret_value $DB_NS postgres-credentials connection_string)")"
  echo "    $DB_NS/vrsky-backup-config BACKUP_DB_URL: $(url_sslmode "$(secret_value $DB_NS vrsky-backup-config BACKUP_DB_URL)")"

  echo "--- clients that mount the CA"
  local d conns n
  conns="$(kubectl get deploy -n "$PLATFORM_NS" -l tier=connector -o name 2>/dev/null | sed 's#.*/##' | sed '/^$/d' || true)"
  n="$(printf '%s\n' "$conns" | sed '/^$/d' | wc -l | tr -d '[:space:]')"
  if [ "$n" -lt "$EXPECTED_CONNECTORS" ]; then echo "    only $n connector Deployments listed, expected $EXPECTED_CONNECTORS"; bad=1; fi
  for d in $CORE_DEPLOYS $conns; do
    has_ca_volume deploy "$d" -n "$PLATFORM_NS" || { echo "    $d: NO $CA_CONFIGMAP volume"; bad=1; }
  done
  cronjob_has_ca || { echo "    cronjob/vrsky-mgmt-backup: NO $CA_CONFIGMAP volume"; bad=1; }
  [ "$bad" = 0 ] && echo "    all mount it" || true
  return 0
}

main() {
  local cmd="${1:-}"; shift || true
  case "$cmd" in
    check) cmd_check ;;
    server) cmd_server ;;
    ca-sync) cmd_ca_sync ;;
    clients) cmd_clients ;;
    urls) cmd_urls "$@" ;;
    canary) cmd_canary ;;
    restart) cmd_restart ;;
    *) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d; s/^# \{0,1\}//' >&2; exit 2 ;;
  esac
}

# Sourced by the guard test (src/pkg/managementapi) to exercise retarget_url.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
