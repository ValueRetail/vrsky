#!/usr/bin/env bash
# affected.sh — which production targets a set of changed paths touches.
#
# Reads changed paths on stdin (one per line: `git diff --name-only A B`) and
# prints one target per line, sorted, unique:
#   image <name>          an image to rebuild in ACR (vrsky/<name>:latest)
#   core <name>           a core service to roll with deploy-core-azure.sh
#   connector <name>      a connector deployment to restart
#   connectors-manifest   the connector manifest changed: full deploy-connectors-azure.sh
#   monitoring            alert rules or monitoring values changed: install-monitoring.sh
#   backup                the backup CronJob or its deploy script changed:
#                         deploy-backup-azure.sh
#
# A changed .go file under src/pkg or src/internal rebuilds the images whose
# main package depends on its package, by `go list` (#437). Whatever the
# mapping can't vouch for rebuilds every Go image: go.mod, go.sum, any
# Dockerfile, a non-.go file under src/pkg or src/internal (go:embed reads
# those), a directory that is no package `go list` knows (deleted, or built
# for no target), and any failure of `go list` itself. When in doubt,
# rebuild. AFFECTED_GO names the go binary (default `go`); the plan job runs
# it with the module cache filled and GOPROXY=off. A _test.go file or anything under a testdata/
# directory below src/ marks nothing on its own (#410). The deploy job feeds
# it `git diff --no-renames`, so a code file renamed to _test.go still
# counts. src/cmd/vrsky-agent and infrastructure/migrations are baked into
# the management-api image.
#
# TOOLS are images with nothing to roll: the backup CronJob pulls
# vrsky-cli:latest at every run, so a rebuild is the whole deploy.
#
# The deploy job in .github/workflows/build-push.yml feeds it a merge's diff.
# Pure text in, text out, so TestAffectedServices drives it with path lists.
# TestAffectedServicesKnowsEveryImage keeps the lists below equal to what
# build-push-acr.sh builds and deploy-core-azure.sh deploys.
set -euo pipefail

CORE="management-api ui data-filter data-converter alert-responder"
CONNECTORS="sitoo-consumer sitoo-producer front-systems-consumer front-systems-producer
business-central-consumer business-central-producer visma-consumer visma-producer
brightpearl-consumer brightpearl-producer sap-s4hana-consumer sap-s4hana-producer
api-consumer webhook-consumer file-consumer db-consumer tenant-consumer
cloud-storage-consumer sftp-consumer kafka-consumer rabbitmq-consumer
salesforce-consumer remote-agent
http-producer db-producer file-producer cloud-storage-producer
sftp-producer kafka-producer rabbitmq-producer salesforce-producer"
TOOLS="vrsky-cli"

# has <list> <name>. The lists span lines, so newlines count as spaces: before
# #437 a name at the start or end of a CONNECTORS line never matched, and a
# change to that connector's own code deployed nothing.
has() { case " ${1//$'\n'/ } " in *" $2 "*) return 0 ;; esac; return 1; }

out=()
# mark <name>: rebuild that image and roll whatever runs it.
mark() {
  if has "$CORE" "$1"; then out+=("image $1" "core $1")
  elif has "$CONNECTORS" "$1"; then out+=("image $1" "connector $1")
  elif has "$TOOLS" "$1"; then out+=("image $1")
  fi
}

GO="${AFFECTED_GO:-go}"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../src" && pwd)"

# go_dependents <dir>...: print every image whose binary links the Go package
# in one of the given directories (relative to src/). It fails, and the
# caller rebuilds everything, when `go list` fails or a directory is no
# package go list knows. The flags match the Dockerfiles: CGO_ENABLED=0,
# linux/amd64, plus the windows/amd64 agent the management-api image builds.
go_dependents() {
  local tmp mains=() n rc=0
  for n in $CORE $CONNECTORS $TOOLS; do [ "$n" = ui ] || mains+=("./cmd/$n"); done
  tmp="$(mktemp -d)" || return 1
  # Chained with &&: `set -e` does not apply inside an `if` or `||` context.
  (
    cd "$SRC" &&
    export CGO_ENABLED=0 GOARCH=amd64 &&
    mod="$("$GO" list -m)" && [ -n "$mod" ] &&
    echo "$mod" > "$tmp/mod" &&
    GOOS=linux   "$GO" list ./... > "$tmp/known" &&
    GOOS=windows "$GO" list ./... >> "$tmp/known" &&
    GOOS=linux   "$GO" list -f '{{.ImportPath}}{{range .Deps}} {{.}}{{end}}' "${mains[@]}" > "$tmp/deps" &&
    GOOS=windows "$GO" list -f "$mod/cmd/management-api{{range .Deps}} {{.}}{{end}}" ./cmd/vrsky-agent >> "$tmp/deps"
  ) || rc=1
  if [ "$rc" = 0 ]; then
    # exit 3: a changed directory is no package go list knows.
    awk -v mod="$(cat "$tmp/mod")" -v changed="$*" '
      FILENAME == ARGV[1] { known[$1] = 1; next }
      { img = $1; sub(/.*\//, "", img); imgs[img] = 1; for (i = 1; i <= NF; i++) dep[img " " $i] = 1 }
      END {
        n = split(changed, c, " ")
        for (i = 1; i <= n; i++) if (!((mod "/" c[i]) in known)) exit 3
        for (img in imgs) for (i = 1; i <= n; i++) if ((img " " mod "/" c[i]) in dep) { print img; break }
      }' "$tmp/known" "$tmp/deps" || rc=1
  fi
  rm -rf "$tmp"
  return "$rc"
}

all_go=0
pkg_dirs=()
while IFS= read -r p; do
  [ -z "$p" ] && continue
  case "$p" in
    ui/*)                                   out+=("image ui" "core ui") ;;
    # Tests and their fixtures never reach a binary: no rebuild, no restart.
    # `*` crosses `/` in a case pattern, so this holds at any depth.
    src/*_test.go|src/*/testdata/*)         ;;
    # A Go file reaches only the images that import its package; any other
    # file there (go:embed assets, assembly, cgo sources) rebuilds them all.
    src/pkg/*.go|src/internal/*.go)         pkg_dirs+=("$(dirname "${p#src/}")") ;;
    src/pkg/*|src/internal/*|src/go.mod|src/go.sum) all_go=1 ;;
    src/cmd/*/Dockerfile)                   all_go=1 ;;
    # The management-api image bakes in the Windows agent it serves for
    # download and the SQL migrations it runs at start (see its Dockerfile).
    src/cmd/vrsky-agent/*|infrastructure/migrations/*)
                                            out+=("image management-api" "core management-api") ;;
    src/cmd/*)
      name="${p#src/cmd/}"; name="${name%%/*}"
      mark "$name" ;;
    infrastructure/prometheus-rules.yml|infrastructure/kubernetes/monitoring/*)
                                            out+=("monitoring") ;;
    infrastructure/azure/deploy-connectors-azure.sh|infrastructure/kubernetes/connectors/*)
                                            out+=("connectors-manifest") ;;
    infrastructure/azure/deploy-backup-azure.sh|infrastructure/kubernetes/backup/*)
                                            out+=("backup") ;;
  esac
done

if [ "$all_go" = 0 ] && [ ${#pkg_dirs[@]} -gt 0 ]; then
  if hits="$(go_dependents "${pkg_dirs[@]}")"; then
    for n in $hits; do mark "$n"; done
  else
    echo "affected.sh: can't map ${pkg_dirs[*]} to images; rebuilding every Go image" >&2
    all_go=1
  fi
fi

if [ "$all_go" = 1 ]; then
  for n in $CORE; do [ "$n" = ui ] && continue; out+=("image $n" "core $n"); done
  for n in $CONNECTORS; do out+=("image $n" "connector $n"); done
  for n in $TOOLS; do out+=("image $n"); done
fi

[ ${#out[@]} -eq 0 ] && exit 0
printf '%s\n' "${out[@]}" | sort -u
