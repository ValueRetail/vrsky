#!/usr/bin/env bash
# deploy-core-azure.sh — roll the core platform images onto AKS by digest.
#
# WHY THIS EXISTS
#
# build-push-acr.sh pushes :latest and prints each digest; nothing consumed
# them. A human copied 64 hex characters into `kubectl set image`, and that step
# failed twice in two days:
#
#   2026-09-07  `kubectl rollout restart` on a digest-pinned deployment. The
#               rollout reported success and redeployed the OLD image, because
#               restarting changes no image reference. Shipped a stale UI bundle
#               that looked deployed.
#   2026-09-08  a placeholder digest pasted literally. Both deployments went to
#               `InvalidImageName`, rollouts hung, and recovery was manual. No
#               outage only because the old replicas stayed up.
#
# Both failure modes come from the same gap, so this script closes it: the
# digest is read from ACR, never typed, and is validated before it reaches the
# cluster.
#
# WHY NOT `kubectl apply`
#
# The manifests under infrastructure/kubernetes/ are shared with the local k3d
# path and name ghcr.io images this cluster cannot pull (see #223). Applying
# them here produces ImagePullBackOff. This script therefore only ever touches
# the image reference of deployments that already exist.
#
# USAGE
#
#   infrastructure/azure/deploy-core-azure.sh                    # all four
#   infrastructure/azure/deploy-core-azure.sh management-api ui  # a subset
#   DRY_RUN=1 infrastructure/azure/deploy-core-azure.sh          # show, don't act
#
# Run build-push-acr.sh first; this deploys whatever :latest currently points at.
set -euo pipefail

REG="${REG:-vrskyprodacr}"
ACR="${REG}.azurecr.io/vrsky"
TIMEOUT="${TIMEOUT:-180s}"
DRY_RUN="${DRY_RUN:-}"

# name namespace deployment — the four images build-push-acr.sh's build_core
# publishes. TestCoreServicesAreBuilt pins this list to that function.
CORE="
management-api vrsky-platform vrsky-management-api
ui             vrsky-ui       vrsky-ui
data-filter    vrsky-platform vrsky-data-filter
data-converter vrsky-platform vrsky-data-converter
"

# Note on ${name} rather than $name below: this script runs under bash, but
# these lines get copied into interactive shells, and in zsh "$x:latest" is
# parsed as the parameter modifier :l — silently yielding "…atest" and an
# unhelpful "the specified tag does not exist" from az. Braces are immune.

# Selected services, as a space-padded string rather than an array: macOS ships
# bash 3.2, where expanding an empty array under `set -u` is itself an error.
WANT=""
for n in "$@"; do WANT="$WANT $n "; done

selected() {
  [ -z "$WANT" ] && return 0
  case "$WANT" in *" $1 "*) return 0 ;; esac
  return 1
}

# Sanity: every requested name has to be in the table, or a typo silently
# deploys nothing and the script still exits 0.
for n in "$@"; do
  if ! awk '{print $1}' <<<"$CORE" | grep -qx "$n"; then
    echo "unknown service '$n' (known: $(awk 'NF{printf "%s ", $1}' <<<"$CORE"))" >&2
    exit 2
  fi
done

echo "Registry : $REG"
echo "Services :${WANT:- all}"
[ -n "$DRY_RUN" ] && echo "Mode     : DRY RUN (no changes)"
echo

failed=0
while read -r name ns deploy; do
  [ -z "$name" ] && continue
  selected "$name" || continue

  echo ">>> $name  ($ns/$deploy)"

  # `|| true` matters: under `set -e` a failing az would abort the script at
  # the assignment, and the validation below — the part that turns a bad digest
  # into a clear message instead of an InvalidImageName pod — would never run.
  digest="$(az acr repository show -n "$REG" --image "vrsky/${name}:latest" --query digest -o tsv 2>/dev/null || true)"
  # The guard that would have caught the placeholder paste. An unset or
  # malformed digest must never reach `set image`: Kubernetes accepts the
  # update, the pods land in InvalidImageName, and the rollout hangs.
  if ! [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "    ERROR: ACR returned no usable digest for vrsky/${name}:latest (got: '${digest}')" >&2
    echo "    Has it been built? infrastructure/azure/build-push-acr.sh core" >&2
    failed=1
    continue
  fi

  current="$(kubectl -n "$ns" get deploy "$deploy" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)"
  if [ -z "$current" ]; then
    echo "    ERROR: deployment $ns/$deploy not found — this script updates existing deployments only" >&2
    failed=1
    continue
  fi

  target="$ACR/${name}@${digest}"
  if [ "$current" = "$target" ]; then
    echo "    already at ${digest:7:12} — nothing to do"
    continue
  fi
  echo "    ${current##*@} -> $digest"

  if [ -n "$DRY_RUN" ]; then
    echo "    would: kubectl -n $ns set image deploy/$deploy '*=$target'"
    continue
  fi

  # '*=' sets every container without needing its name. The names do not match
  # the deployment (vrsky-ui's container is "ui"), which is what makes a
  # name-keyed strategic-merge patch fail with "image: Required value".
  kubectl -n "$ns" set image "deploy/$deploy" "*=$target" >/dev/null

  if ! kubectl -n "$ns" rollout status "deploy/$deploy" --timeout="$TIMEOUT"; then
    echo "    ROLLOUT FAILED. The previous replicas keep serving until the new" >&2
    echo "    ones are Ready, so this is recoverable:" >&2
    echo "      kubectl -n $ns get pods -l app=$deploy" >&2
    echo "      kubectl -n $ns rollout undo deploy/$deploy" >&2
    failed=1
    continue
  fi

  # Verify against the cluster rather than trusting the command above. A
  # rollout can report success while serving something else entirely — that is
  # the 2026-09-07 failure, and it is exactly what a deploy script should be
  # the one to notice.
  live="$(kubectl -n "$ns" get deploy "$deploy" -o jsonpath='{.spec.template.spec.containers[0].image}')"
  if [ "$live" != "$target" ]; then
    echo "    ERROR: after rollout the deployment reads $live, expected $target" >&2
    failed=1
    continue
  fi
  echo "    deployed and verified"
done <<<"$CORE"

echo
if [ "$failed" -ne 0 ]; then
  echo "One or more services did not deploy. Nothing was rolled back automatically."
  exit 1
fi
[ -n "$DRY_RUN" ] && exit 0

cat <<EOF
Core services deployed.

Spot-check what is actually being served (not just what the spec says):
  kubectl -n vrsky-platform exec deploy/vrsky-management-api -- \\
    curl -s localhost:3000/openapi.json | head -c 80
  kubectl -n vrsky-ui exec deploy/vrsky-ui -- ls /usr/share/nginx/html/assets

The UI asset filename is a content hash, so it changes whenever the bundle
does — comparing it against the build output is the cheapest proof that the
browser will get the new code.
EOF
