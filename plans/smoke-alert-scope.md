# Smoke check: VRSky's alerts and the critical ones, not every stock warning

Seen 2026-10-20: the deploy job for #330 rolled everything, every consumer
restored its pipelines, and the smoke check still failed because
`KubeClientErrors` (a kube-prometheus-stack warning about the API server's
own client calls) fired while thirty connectors restarted at once. It had
never fired before, it cleared twenty minutes later, and the re-run was
green. A red deploy for that is noise of the kind #326 removed for `info`.

## Open questions

1. **Which firing alerts fail a deploy.** Recommended: **every alert in
   a `vrsky-*` rule group above `info`, plus any alert at severity
   `critical` from any group.** VRSky's own rules are what a deploy can
   break (`ConnectorUnavailable`, `MgmtAPIErrorRate`, `PipelineDown`,
   `ConnectionInError`); a critical stock alert (`KubeAPIDown`,
   `KubeletDown`, `NodeFilesystemAlmostOutOfSpace`, `PrometheusBadConfig`)
   is worth stopping for whoever caused it. Stock warnings
   (`KubeClientErrors`, `NodeDiskIOSaturation`, `KubeDeploymentReplicasMismatch`
   mid-rollout) stop counting. Alternative: VRSky's groups only, which
   would let a deploy report ok with the API server down; the critical
   clause costs nothing.
2. **Make the filter testable.** Recommended: **yes**, move it into
   `infrastructure/azure/smoke-alerts.sh`, which reads Prometheus's
   `/api/v1/rules` JSON on stdin and prints the alert names that count,
   one per line. `smoke-prod.sh` pipes the API into it. A Go table test
   feeds it fixture JSON. Alternative: keep the jq inline and trust it,
   which is how today's rule got there.

## Goal

A deploy is red when VRSky is broken or the cluster is in a critical
state, and green through the warnings that a fleet restart produces by
itself. The check stays one script an operator can run by hand.

## What exists (checked)

- `smoke-prod.sh`: readyz and UI 200, "Restored running pipelines" per
  consumer, then `/api/v1/alerts` filtered to `state == firing`,
  `severity != info`, minus `Watchdog` and `InfoInhibitor`. The alerts
  endpoint carries labels but not the rule group.
- `/api/v1/rules` carries the group name, each rule's labels and its
  active alerts with state; VRSky's groups are `vrsky-pipelines` and
  `vrsky-platform` (`infrastructure/prometheus-rules.yml`).
- `Watchdog` sits in `general.rules` at severity `none`, `InfoInhibitor`
  at `none` too: neither is in a `vrsky-*` group nor critical, so the
  special cases disappear.
- The in-cluster runtime has jq (no sed/python); the guard
  `TestInClusterScriptsUseOnlyRuntimeTools` covers every script the jobs
  run through `in-cluster.sh`, so the new script is covered once
  `smoke-prod.sh` calls it.
- `TestCatchUpWorkflow` only requires that `smoke-prod.sh` is run.

## Approach

### 1. `infrastructure/azure/smoke-alerts.sh`

```bash
# Reads Prometheus /api/v1/rules on stdin; prints the firing alerts that
# fail a deploy, sorted, one per line: everything in a vrsky-* group above
# info, and anything critical from any group.
jq -r '
  [ .data.groups[]
    | .name as $group
    | .rules[]
    | select(.type == "alerting")
    | select(($group | startswith("vrsky-")) or .labels.severity == "critical")
    | select(.labels.severity != "info")
    | .alerts[]?
    | select(.state == "firing")
    | .labels.alertname
  ] | unique | .[]'
```

### 2. `smoke-prod.sh`

The alerts step becomes `kubectl get --raw …/api/v1/rules | bash
smoke-alerts.sh`, same output lines as today ("alerts firing: …" /
"alerts: none"); the header comment says what counts and why.

### 3. Docs

`docs/operator/deploy.md` (Watching a deploy) and `docs/operator/monitoring.md`:
one sentence each on what the smoke check counts.

## Files

| File | Change |
|---|---|
| `infrastructure/azure/smoke-alerts.sh` (new) | the filter |
| `infrastructure/azure/smoke-prod.sh` | call it; comment |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestSmokeAlertScope`, and `TestSmokeProdUsesAlertScope` (the smoke script calls the filter and no longer reads `/api/v1/alerts`) |
| `docs/operator/deploy.md`, `docs/operator/monitoring.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestSmokeAlertScope` (fixture rules JSON): `KubeClientErrors` warning firing → nothing; `ConnectorUnavailable` firing → counted; `PipelineDown` pending → nothing; `KubeAPIDown` critical firing → counted; a `vrsky-pipelines` rule at `info` firing → nothing; `Watchdog` firing → nothing; two firing alerts → both, sorted | the filter exactly | drop the `critical` clause; drop the `info` exclusion; count `pending` |
| `TestSmokeProdUsesAlertScope` | `smoke-prod.sh` pipes `/api/v1/rules` into `smoke-alerts.sh` and no longer filters `/api/v1/alerts` inline | inline the old jq again |

Then shellcheck-by-eye, `go test ./pkg/managementapi/ -run Smoke`, and the
existing guards.

## Verify

Locally against prod (read-only): `kubectl get --raw …/rules | bash
infrastructure/azure/smoke-alerts.sh` prints nothing while nothing of
VRSky's fires, and `bash infrastructure/azure/smoke-prod.sh` says ok. The
PR's merge deploys; its smoke check runs the new filter.

## Risks

- **A stock warning that mattered** (say `KubePodCrashLooping` on a
  connector) no longer fails the deploy by itself; `ConnectorUnavailable`
  and the restored-pipelines check cover the VRSky side, and the warning
  still reaches Teams through Alertmanager as today.

## Non-goals

Changing any alert's severity or routing; waiting for pending alerts to
settle; a per-deploy "new since the rollout started" comparison.
