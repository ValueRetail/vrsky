#!/usr/bin/env bash
# budget-check.sh — the Production check's Budget step (plans/cost-cap.md,
# plans/issue-744-budget-check.md).
#
#   MONTHLY_BUDGET_NOK=1500 AKS_RESOURCE_GROUP=vrsky-prod \
#     budget-check.sh <subscription-id>
#
# Reads the month-to-date cost of each VRSky resource group at the group's
# own scope (the deploy identity holds Cost Management Reader per group, not
# on the subscription), sums them and hands the sum to budget.sh. Then:
#   below the budget        → the line in the job summary, exit 0
#   at or above it          → az aks stop, an error annotation, exit 1
#   a query throttled (429) → retried after 10, 30 and 60 s; still throttled:
#                             a warning, no verdict, exit 0 (the next run
#                             checks again)
#   any other failure       → the HTTP status and error code in the log, an
#                             error annotation, nothing stopped, exit 1
#
# `az rest` prints no response headers, so Retry-After is not readable
# without fetching a token ourselves; the fixed backoff stands in for it.
# BUDGET_RETRY_DELAYS overrides the delays (the tests set "0 0 0").
set -euo pipefail

if [ $# -ne 1 ] || [ -z "$1" ]; then
  echo "usage: budget-check.sh <subscription-id>" >&2
  exit 2
fi
sub=$1
: "${MONTHLY_BUDGET_NOK:?MONTHLY_BUDGET_NOK is not set}"
: "${AKS_RESOURCE_GROUP:?AKS_RESOURCE_GROUP is not set}"
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
read -r -a delays <<< "${BUDGET_RETRY_DELAYS:-10 30 60}"

groups=(vrsky-prod MC_vrsky-prod_vrsky-prod_norwayeast)
body='{"type":"ActualCost","timeframe":"MonthToDate","dataset":{"granularity":"None","aggregation":{"totalCost":{"name":"Cost","function":"Sum"}}}}'

errf=$(mktemp)
trap 'rm -f "$errf"' EXIT

# http_status maps the reason phrase `az rest` prints on a failed request
# ("ERROR: Too Many Requests({...})") to its number.
http_status() {
  case "$1" in
    "Bad Request") echo 400 ;;
    "Unauthorized") echo 401 ;;
    "Forbidden") echo 403 ;;
    "Not Found") echo 404 ;;
    "Too Many Requests") echo 429 ;;
    "Internal Server Error") echo 500 ;;
    "Bad Gateway") echo 502 ;;
    "Service Unavailable") echo 503 ;;
    "Gateway Timeout") echo 504 ;;
    *) echo unknown ;;
  esac
}

# query_group sets cost (the group's month-to-date cost) and returns 0, or
# returns 4 when still throttled after the retries, or 1 on any other
# failure. Everything it says goes to stderr, never a token: az rest's error
# text is the response's reason phrase and body.
query_group() {
  local rg=$1 url attempt=0 out msg reason status code detail
  url="https://management.azure.com/subscriptions/$sub/resourceGroups/$rg/providers/Microsoft.CostManagement/query?api-version=2023-11-01"
  while :; do
    if out=$(az rest --method post --url "$url" --body "$body" --query 'properties.rows[0][0]' -o tsv 2>"$errf"); then
      cost=${out:-0}   # no rows yet this month
      return 0
    fi
    msg=$(grep -m1 'ERROR' "$errf" || head -n1 "$errf" || true)
    reason="" code="" detail=""
    if [[ "$msg" =~ ^(ERROR:[[:space:]]*)?([A-Za-z][A-Za-z ]*)\( ]]; then
      reason=${BASH_REMATCH[2]}
    fi
    status=$(http_status "$reason")
    if [[ "$msg" =~ \"code\":[[:space:]]*\"([^\"]*)\" ]]; then
      code=${BASH_REMATCH[1]}
    fi
    if [ "$status" = unknown ] && [[ "$code" =~ ^[0-9]{3}$ ]]; then
      status=$code
    fi
    if [[ "$msg" =~ \"message\":[[:space:]]*\"([^\"]*)\" ]]; then
      detail=${BASH_REMATCH[1]:0:300}
    fi
    echo "budget: cost query for $rg failed: HTTP $status${reason:+ ($reason)}, code ${code:-none}${detail:+: $detail}" >&2
    if [ "$status" != 429 ]; then
      return 1
    fi
    if [ "$attempt" -ge "${#delays[@]}" ]; then
      return 4
    fi
    echo "budget: throttled; retry $((attempt + 1)) of ${#delays[@]} for $rg in ${delays[$attempt]}s" >&2
    sleep "${delays[$attempt]}"
    attempt=$((attempt + 1))
  done
}

total=0
failed=""
throttled=""
# az's tsv can print a credit (-12.5) or a tiny amount in exponent form (1e-05).
num='^-?[0-9]+([.][0-9]*)?([eE][-+]?[0-9]+)?$'
for rg in "${groups[@]}"; do
  cost=""
  set +e
  query_group "$rg"; rc=$?
  set -e
  case $rc in
    0)
      if ! [[ "$cost" =~ $num ]]; then
        echo "budget: cost for $rg is not a number ('$cost')" >&2
        failed="$failed $rg"
        continue
      fi
      echo "budget: $rg month to date: $cost NOK"
      total=$(awk -v a="$total" -v b="$cost" 'BEGIN { printf "%.2f", a + b }') ;;
    4) throttled="$throttled $rg" ;;
    *) failed="$failed $rg" ;;
  esac
done

if [ -n "$failed" ]; then
  echo "::error title=Budget check failed::The cost query failed for${failed} (status above). Nothing was stopped; does the deploy identity hold Cost Management Reader on the resource groups?"
  exit 1
fi
if [ -n "$throttled" ]; then
  echo "::warning title=Budget not checked::Cost Management kept throttling (429) the query for${throttled} after ${#delays[@]} retries; no verdict this run, the next run checks again."
  { echo "## Budget not checked"; echo; echo "Cost Management throttled the query for${throttled}; the next run checks again."; } >> "$summary"
  exit 0
fi

set +e
line=$("$here/budget.sh" "$total" "$MONTHLY_BUDGET_NOK" 2>&1); code=$?
set -e
echo "$line"
case $code in
  0) echo "$line" >> "$summary" ;;
  3) az aks stop -g "$AKS_RESOURCE_GROUP" -n vrsky-prod
     echo "::error title=Monthly budget reached::$line. The cluster has been stopped; it will be stopped again on every run this month."
     { echo "## Monthly budget reached"; echo; echo "$line"; echo; echo "The cluster has been stopped and stays parked until next month (or a higher MONTHLY_BUDGET_NOK)."; } >> "$summary"
     exit 1 ;;
  *) echo "::error title=Budget check failed::$line. Nothing was stopped."
     exit 1 ;;
esac
