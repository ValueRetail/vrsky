# Dead man's switch: who watches the monitoring

Written 2026-10-20. Every alert VRSky has goes Prometheus → Alertmanager →
management-api → Teams. If Prometheus or Alertmanager dies, or the rule
file stops loading, the channel goes quiet in exactly the way "all is
well" looks. The chart's `Watchdog` alert fires forever for this purpose
and is routed to `null` (`prometheus-values.azure.yaml`): nothing reads
it.

## Open questions

1. **Who reads the Watchdog.** Recommended: **the Drift check workflow**,
   which already runs every 30 minutes with the right gate (a stopped
   cluster exits green). One more in-cluster step asks Prometheus whether
   `Watchdog` is firing and Alertmanager whether it has received it; if
   either says no, the run fails and the *CI failure alert* opens the
   stateful issue, the same signal a red `main` gives, and closes it when
   the next run is green. No new account, no new secret, parking-aware by
   construction. Alternative: an external receiver (healthchecks.io or
   similar) that Alertmanager pings with the Watchdog and that pages when
   pings stop: a few minutes' latency instead of thirty, and it covers the
   case where GitHub Actions itself is down, but every `az aks stop` would
   page unless the check is paused first, which means an API key on the
   laptop and one more step in the parking routine.
2. **How far down the chain.** Recommended: **Prometheus and Alertmanager
   only.** The management-api's webhook and the Teams target cannot be
   exercised without sending a card, and the API's own health is already
   observed by `/readyz` in the smoke check and by `MgmtAPIErrorRate`.
   Alternative: a monthly test card, which is noise by design.
3. **The issue's title.** The failure alert names the workflow: "CI red on
   main: Drift check". Recommended: **rename the workflow to "Production
   check"**, since it now answers two questions, and update the failure
   alert's list. Alternative: keep the name and live with a slightly odd
   title.

## Goal

A dead Prometheus or Alertmanager is noticed within about half an hour
while the cluster runs, as an issue that closes itself when they are
back. A parked cluster raises nothing.

## What exists (checked)

- `Watchdog` (`general.rules`, severity `none`) fires continuously;
  Alertmanager routes it to the `null` receiver, `repeat_interval` 24 h.
- `drift-check.yml`: `production` environment and group, OIDC login,
  gate step (`powerState.code`, build in progress), then in-cluster steps.
  `TestDriftCheckWorkflow` requires every post-gate step to be gated and
  every kubectl to go through `in-cluster.sh`; it does not limit the
  number of steps.
- `ci-failure-alert.yml` lists the workflows it watches by name;
  `TestCIFailureAlertWatchesEveryMainWorkflow` keeps the list complete.
- In-cluster runtime: bash, curl, jq, kubectl. Services:
  `prometheus-prometheus:9090` and `prometheus-alertmanager:9093` in
  `vrsky-monitoring` (the port-forward hints in `install-monitoring.sh`).
- The smoke check reads Prometheus through the API server proxy
  (`kubectl get --raw …/proxy/…`), which also works for Alertmanager.

## Approach

### 1. `infrastructure/azure/monitoring-alive.sh` (in-cluster)

```bash
# Exit 0 when Prometheus reports Watchdog firing and Alertmanager has it;
# otherwise print what is missing and exit 1.
P=/api/v1/namespaces/vrsky-monitoring/services/prometheus-prometheus:9090/proxy/api/v1
A=/api/v1/namespaces/vrsky-monitoring/services/prometheus-alertmanager:9093/proxy/api/v2
prom=$(kubectl get --raw "$P/query?query=ALERTS%7Balertname%3D%22Watchdog%22%2Calertstate%3D%22firing%22%7D" | jq '.data.result | length')
am=$(kubectl get --raw "$A/alerts?filter=alertname%3D%22Watchdog%22" | jq 'length')
```

`prom >= 1` and `am >= 1` → "monitoring alive"; else the missing side,
exit 1. A Prometheus that is down makes `kubectl get --raw` fail, which
is the same exit 1. Pure bash and jq; covered by
`TestInClusterScriptsUseOnlyRuntimeTools` once the workflow runs it.

### 2. The workflow

`drift-check.yml` → name "Production check", file name unchanged; a step
*Monitoring alive* after the gate, before the digest comparison:
`infrastructure/azure/in-cluster.sh stage "bash infrastructure/azure/monitoring-alive.sh"`,
gated like the others. The stage step copies the script. A failure fails
the job; the drift comparison still runs on its own `if: always()` guard
so one problem does not hide the other.

`ci-failure-alert.yml`: "Drift check" → "Production check".

### 3. Docs

`docs/operator/monitoring.md`: a short *Who watches the monitoring*
paragraph. `docs/operator/deploy.md`: the Drift check paragraph mentions
the heartbeat and the new name.

## Files

| File | Change |
|---|---|
| `infrastructure/azure/monitoring-alive.sh` (new) | the check |
| `.github/workflows/drift-check.yml` | name; the step; staging |
| `.github/workflows/ci-failure-alert.yml` | the name |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestMonitoringAliveScript` (table over fixture responses: both present → ok; Watchdog absent in Prometheus → fail naming Prometheus; absent in Alertmanager → fail naming Alertmanager; Prometheus unreachable → fail), `TestDriftCheckWorkflow` extended (runs the script, gated, before the comparison) |
| `docs/operator/monitoring.md`, `docs/operator/deploy.md` | as above |

The script reads kubectl through a `KUBECTL` variable defaulting to
`kubectl`, so the test points it at a stub that serves fixture JSON per
path; same trick as a fake `az` would be, and no cluster in the test.

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestMonitoringAliveScript` | the four outcomes above | drop the Alertmanager half; treat an empty result as alive |
| `TestDriftCheckWorkflow` | the heartbeat step exists, is gated, runs in-cluster, precedes the comparison; the comparison runs even when it fails | remove the step; drop its `if` |
| `TestCIFailureAlertWatchesEveryMainWorkflow` | the renamed workflow is watched | — |

## Verify

1. Merge; the next scheduled run says "monitoring alive" and "production
   is at main".
2. Provoke it: `kubectl -n vrsky-monitoring scale statefulset
   alertmanager-prometheus-alertmanager --replicas=0`, dispatch the
   workflow: red, issue "CI red on main: Production check" names
   Alertmanager. Scale back to 1, dispatch again: green, issue closed.
3. Stop the cluster: the scheduled run is green with the "Stopped" notice.

## Risks

- **Thirty minutes of silence** before it is noticed, plus GitHub's cron
  jitter. Acceptable for a stack carrying ~100 messages a day; the
  external receiver is the answer if that ever changes.
- **GitHub Actions down** means no check and no issue. Same as every
  other signal this repository has.

## Non-goals

Exercising the management-api webhook or the Teams target; an external
receiver; Alertmanager high availability.
