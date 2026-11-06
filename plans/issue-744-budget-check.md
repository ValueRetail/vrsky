# Issue #744: the Production check's Budget step fails whenever it runs

## Problem

The Budget step in `.github/workflows/drift-check.yml` went red on every
run that reached it (2026-11-02, 2026-11-03) with
`month-to-date cost is not a number ('query failed')`. Two likely causes,
neither visible in the log because the step threw stderr away
(`2>/dev/null || echo "query failed"`):

- It queried at subscription scope with a resource-group filter, while the
  deploy identity holds Cost Management Reader only on the two resource
  groups. That is probably a 401/403 every time.
- Cost Management throttles hard; Ludvik's own RG-scope query got a 429 on
  2026-11-03.

## Acceptance criteria

1. A failed query logs its HTTP status and error code (no tokens).
2. Each resource group is queried at its own scope
   (`/subscriptions/{id}/resourceGroups/{rg}/providers/Microsoft.CostManagement/query`)
   and the two are summed, so the RG-scoped role is enough.
3. A 429 is retried after a backoff, up to 3 times. Still throttled: a
   `::warning::`, no verdict, exit 0. Any other failure still fails the run.
4. At or over `MONTHLY_BUDGET_NOK` the cluster is stopped as before.
5. Tests run the logic against a stub `az`: two scopes summed, over budget,
   429 then success, 429 throughout, 403. A guard test checks the workflow
   calls the script.

## Approach

- New `infrastructure/azure/budget-check.sh <subscription-id>`, reading
  `MONTHLY_BUDGET_NOK` and `AKS_RESOURCE_GROUP` from the environment. It
  holds what the step did inline, plus the per-RG query, the retry and the
  error logging. The verdict stays in `budget.sh`.
- The Budget step becomes one line calling the script. Its `if`, `env`,
  the job's permissions and triggers are unchanged.
- `az rest` does not expose response headers, so `Retry-After` cannot be
  read without fetching a bearer token and calling curl ourselves. That
  would put a token in a shell variable, so the script backs off instead:
  10, 30, 60 seconds (`BUDGET_RETRY_DELAYS`, overridable for tests). The
  status comes from the reason phrase `az rest` prints
  (`ERROR: Too Many Requests({"error":{"code":...}})`), mapped to a number,
  and the error code from the JSON body.
- Both groups are always queried, so a 403 on one is not hidden by a 429
  on the other. Any non-429 failure fails the run; if none failed but one
  stayed throttled, the run warns and stays green.

## Files

| File | Change |
|---|---|
| `infrastructure/azure/budget-check.sh` (new) | query, retry, sum, verdict, stop |
| `.github/workflows/drift-check.yml` | Budget step calls the script |
| `src/pkg/managementapi/budget_check_script_test.go` (new) | stub `az` tests |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestDriftCheckWorkflow` follows the step into the script |
| `docs/operator/cost-parking.md`, `plans/cost-cap.md` | describe the per-group query and throttling |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| sum under budget | two RG scopes queried, values summed, exit 0, no stop | query one group only; subscription scope |
| over budget | `az aks stop -g $AKS_RESOURCE_GROUP -n vrsky-prod`, exit 1 | drop the stop |
| 429 then success | retried, verdict reached | no retry |
| 429 throughout | `::warning::`, exit 0, no stop, 4 attempts | treat throttling as a failure |
| 403 | exit 1, status 403 and code in the log, no stop | swallow stderr |
| empty rows | counts as 0 | — |
| workflow guard | the Budget step runs the script, gated, no `2>/dev/null` | inline the old query back |

The stub sits on `PATH=<stub>:/usr/bin:/bin`; the test fails if `az`
resolves anywhere else, and checks the stub's log is non-empty.

## Risks

- If a future `az` changes its error text, the status reads `unknown`; the
  run still fails (not 429), so nothing is silently skipped.
- A month where every run is throttled gives no verdict at all. The
  warning shows on each run; the Azure budget e-mail still fires.
- A fully throttled run waits 10 + 30 + 60 s per group, so it holds the
  `production` concurrency group for up to ~200 s; a deploy queued behind
  it waits that long.
- One group over budget on its own while the other is throttled gives no
  verdict, on purpose: the script only decides on a complete sum, and the
  next run (or the one after) has it.
- Az's tsv can print a credit (`-12.25`) or an exponent (`1e-05`); both
  are accepted. A negative total still fails in `budget.sh`, which wants a
  non-negative number; that would take a month of credits larger than
  the spend.

## Verify

`bash -n`, `go test -count=1 -run 'BudgetCheck|DriftCheckWorkflow|BudgetVerdict' ./pkg/managementapi/`,
`go vet`, `golangci-lint run`, `actionlint`. The real check is the next
Production check run while the cluster is up (after merge, needs a human:
workflow change).
