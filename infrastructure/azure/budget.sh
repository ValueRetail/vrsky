#!/usr/bin/env bash
# budget.sh — is this month's spend under the budget? (plans/cost-cap.md)
#
#   budget.sh <month-to-date-cost> <monthly-budget>
#
# Both in the same currency (NOK). Prints one line with the numbers and the
# percentage, then:
#   exit 0  below the budget
#   exit 3  at or above it — the caller stops the cluster
#   exit 2  the cost is not a number (the query failed, or the identity may
#           not read costs) — the caller fails loudly and stops NOTHING; a
#           broken query must never park production.
# Pure arithmetic, table-tested by TestBudgetVerdict.
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: budget.sh <month-to-date-cost> <monthly-budget>" >&2
  exit 2
fi
mtd=$1
budget=$2
num='^[0-9]+([.][0-9]+)?$'
if ! [[ "$mtd" =~ $num ]]; then
  echo "budget: month-to-date cost is not a number ('$mtd'); no verdict" >&2
  exit 2
fi
if ! [[ "$budget" =~ $num ]] || [ "${budget%%.*}" = 0 ]; then
  echo "budget: monthly budget is not a positive number ('$budget'); no verdict" >&2
  exit 2
fi

pct=$(awk -v m="$mtd" -v b="$budget" 'BEGIN { printf "%d", (m / b) * 100 }')
printf 'budget: spent %.0f of %.0f NOK this month (%s%%)\n' "$mtd" "$budget" "$pct"
if awk -v m="$mtd" -v b="$budget" 'BEGIN { exit !(m >= b) }'; then
  echo "budget: reached" >&2
  exit 3
fi
