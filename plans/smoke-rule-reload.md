# Smoke check: give a just-applied rule change time to take effect

Seen 2026-10-22, run 39764021090 (merge of #346): the deploy applied a
corrected `NodeCPURequestsHigh` rule at 10:53:21, the smoke check read
the alerts at 10:53:29 and failed on the old rule's alert; Prometheus
loaded the new rule and evaluated it inactive at 10:54:03. A re-run went
green. The same happens to any merge that fixes or removes an alert that
is firing, which is exactly the merge that should go green.

## Open questions

1. **Retry, or wait for the reload?** Recommended: **retry**. When alerts
   that count are firing, `smoke-prod.sh` re-reads them every 15 seconds
   for up to 2 minutes, prints each round, and fails only if something
   still fires at the end. A real problem persists through two minutes;
   a reload artefact clears within one evaluation (30 s) after the
   operator reloads Prometheus (seen: ~40 s). It also absorbs an alert
   that resolves a beat after a rollout. Alternative: make
   `install-monitoring.sh` wait until Prometheus serves the applied rule
   expressions, which is exact but compares rule text across YAML and the
   API, more code to get right.
2. **Retry always, or only after the monitoring stage ran?** Recommended:
   **always**. Costs nothing when nothing fires, and a run by hand after a
   rollout benefits the same way. A clean run still takes one read.
3. **Window.** Recommended: **120 s, every 15 s**, overridable through
   `SMOKE_ALERT_WINDOW` / `SMOKE_ALERT_EVERY` (seconds) so a test can
   shorten them. Four times the evaluation interval plus the reload.

## Goal

A deploy that fixes a firing alert goes green on its own run; a deploy
that leaves something real firing still fails, about two minutes later
than today.

## Approach

`smoke-prod.sh`, the alert section only:

```bash
alert_window="${SMOKE_ALERT_WINDOW:-120}"; alert_every="${SMOKE_ALERT_EVERY:-15}"
waited=0
while :; do
  firing=$(kubectl get --raw "…/api/v1/rules" 2>/dev/null | bash smoke-alerts.sh 2>/dev/null | tr '\n' ' ' || echo "")
  firing="${firing% }"
  [ -z "$firing" ] && break
  [ "$waited" -ge "$alert_window" ] && break
  echo "alerts firing: $firing (re-checking in ${alert_every}s, ${waited}s of ${alert_window}s)"
  sleep "$alert_every"; waited=$((waited + alert_every))
done
```

then the same final verdict line as today. `smoke-alerts.sh` stays the
only place that decides what counts; bash only (in-cluster runtime).
`KUBECTL` defaults to `kubectl` so a test can stand a stub in (the same
seam as `monitoring-alive.sh`). The two `curl` probes take the URL from
`PUBLIC_URL` already; the test points it at a local stub server or skips
them (see Tests).

## Files

| File | Change |
|---|---|
| `infrastructure/azure/smoke-prod.sh` | the loop; `KUBECTL` seam; header comment |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestSmokeProdWaitsOutARuleReload` |
| `docs/operator/deploy.md` | one sentence in the smoke paragraph |

## Tests

`TestSmokeProdWaitsOutARuleReload` runs `smoke-prod.sh` with a stub
`kubectl` (answers `get --raw …/rules` from a counter file: a firing
VRSky alert for the first N calls, then nothing) and an `httptest`
server as `PUBLIC_URL` answering 200 to `/readyz` and `/`, with
`SMOKE_ALERT_WINDOW=2`, `SMOKE_ALERT_EVERY=1`:

| Case | Expect | Mutation that must fail it |
|---|---|---|
| firing for one read, then clear | exit 0, "smoke: ok", a "re-checking" line | remove the loop (one read) |
| firing on every read | exit 1, "alerts firing:", after ≥ 2 s | treat the end of the window as clean |
| nothing firing | exit 0 after one read, no "re-checking" line | — |

Existing guards stay green: `TestSmokeProdUsesAlertScope`,
`TestSmokeAlertScope`, `TestInClusterScriptsUseOnlyRuntimeTools`.

## Verify

The PR's merge runs the smoke check with the loop; with nothing firing
it is one read, as today. A by-hand run against production says ok.

## Non-goals

Changing what counts (`smoke-alerts.sh`); waiting on anything else in the
smoke check; the consumer "restored" checks (they already retry for 60 s).
