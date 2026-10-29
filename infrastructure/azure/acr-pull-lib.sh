# acr-pull-lib.sh — copy vrsky-platform's acr-pull Secret into another
# namespace without ever printing it (#496). Sourced, not run, by
# copy-acr-pull.sh, deploy-backup-azure.sh and deploy-ui-azure.sh.
#
# bash, jq and kubectl only: the first two run inside the cluster through
# in-cluster.sh, where there is no sed.

# apply_acr_pull <secret-json> <namespace>
#
# <secret-json> is acr-pull as `kubectl get secret -o json` prints it. The
# source's identity (uid, resourceVersion, ownerReferences, labels,
# annotations) stays behind: apply would refuse a foreign uid.
#
# The deploy log is public to anyone with repo access, and a failed apply on
# an existing object can print "error when applying patch:" with the patch,
# which holds the new .dockerconfigjson. So the output is captured, and on a
# failure only a generic line and the exit code are printed. Returns
# kubectl's exit code; callers run it as a plain command so `set -e` stops
# them.
apply_acr_pull() {
  local pull="$1" ns="$2" out rc=0
  # pipefail inside the substitution, whatever the caller set: a failed jq
  # must not pass for an apply of nothing.
  out="$( set -o pipefail; { printf '%s' "$pull" \
    | jq --arg ns "$ns" '{apiVersion: "v1", kind: "Secret", type: .type, data: .data,
                          metadata: {name: "acr-pull", namespace: $ns}}' \
    | kubectl apply -f - ; } 2>&1 )" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "ERROR: apply of $ns/acr-pull failed (exit $rc); output withheld because it can contain the Secret" >&2
    return "$rc"
  fi
  printf '%s\n' "$out"
}
