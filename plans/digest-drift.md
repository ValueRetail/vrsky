# Digest drift: production behind main, noticed and fixed by itself

Written 2026-10-20. `plans/deploy-while-parked.md` listed the risk: a start
without a Catch up leaves production one or more merges behind, silently.
Nothing watches for it today.

## Open questions

1. **Report or repair?** Recommended: **repair.** When the check finds a
   service running a digest other than the registry's `:latest`, it
   dispatches the existing **Catch up** workflow (connectors restart only
   when a connector drifted) and the run says what it found. You chose
   "fully auto" for merges; a start is the same decision an hour later.
   Alternative: fail the run, so the *CI failure alert* files an issue and
   a Teams card, and you click Catch up yourself.
2. **How often.** Recommended: **every 30 minutes** (`schedule`), plus
   `workflow_dispatch`. A stopped cluster costs one `az aks show` and
   exits green in under a minute, about 50 runner-minutes a day; GitHub's
   cron can run late by some minutes, which does not matter here.
   Alternative: hourly.
3. **What counts as drift.** Recommended: **the digest a pod is actually
   running** (`status.containerStatuses[].imageID`) against the registry's
   `:latest` for the same repository, for every `vrsky-*` pod in
   `vrsky-platform` and `vrsky-ui`. This covers the core services (set by
   digest) and the connectors (tag `:latest`, picked up only on restart)
   with one rule, and it is exactly what was verified by hand today
   (running and registry digests compare equal for a core service and a
   connector). Alternative: compare Deployment specs, which says nothing
   about connectors.

## Goal

After `az aks start`, production is at `main` within about half an hour
with no click, and a run log says which services were behind. While
nothing drifts, nothing happens.

## What exists (checked)

- `catch-up.yml`: dispatch-only, `production` environment and concurrency
  group, refuses a stopped cluster, rolls core by digest, restarts the fleet
  (input `connectors`), monitoring, smoke. The repair is a dispatch of this.
- The deploy identity holds `Contributor` on `vrskyprodacr` and on the
  cluster: `az acr repository show-tags` and `az aks command invoke` both
  work for it (the catch-up's stage step does the former already).
- The in-cluster runtime has kubectl and jq (no sed/python; guarded).
- `az acr build` re-points `:latest` while the `build-images` matrix runs,
  **before** the deploy job rolls anything. The matrix is not in the
  `production` concurrency group, so a check at that moment would see
  drift that the deploy job is about to fix.

## Approach

### 1. `infrastructure/azure/drift.sh`: the comparison, pure text

Input: a file of `name running-digest` lines (one per pod, duplicates
allowed: two replicas, or old and new pod during a rollout) and a file of
`name latest-digest` lines from the registry. Output: one line per drifted
service, `name running latest`, sorted; exit 0 either way. A name with no
registry entry is reported as `name running (not in registry)` so a renamed
image cannot hide. Go table test, like `affected.sh`.

### 2. `.github/workflows/drift-check.yml`

```yaml
on:
  schedule: [{ cron: "*/30 * * * *" }]
  workflow_dispatch:
jobs:
  drift:
    environment: production
    concurrency: { group: production, cancel-in-progress: false }   # queues behind a deploy or catch-up
    permissions: { id-token: write, contents: read, actions: write } # actions: write to dispatch Catch up
    steps:
      - checkout; azure/login
      - Cluster power state: Stopped → notice, exit 0
      - A build on main in progress? (gh run list --workflow build-push.yml --branch main --status in_progress) → notice, exit 0
      - Running digests: in-cluster.sh stage "kubectl get pods -n vrsky-platform -n vrsky-ui -o json | jq -r '…app, imageID…'" > running.txt
      - Registry digests: az acr repository list + show-tags per repo (one call per repository, ~36) > latest.txt
      - drift.sh running.txt latest.txt > drift.txt
      - if empty: "production is at main"; else: job summary with the table,
        gh workflow run catch-up.yml -f connectors=<true if any name is not core>, notice "Catch up dispatched"
```

The check never touches the cluster itself; the only write is the
dispatch. The `production` group means it cannot run while a deploy or a
catch-up is rolling, and the in-progress-build guard covers the window
before the deploy job joins the group.

### 3. Docs

- `docs/operator/cost-parking.md` *Start*: "within about half an hour the
  drift check brings production to main by itself; Catch up stays as the
  immediate button".
- `docs/operator/deploy.md` *While the cluster is parked*: the same, plus
  how to read a drift run.

## Files

| File | Change |
|---|---|
| `infrastructure/azure/drift.sh` (new) | the comparison |
| `.github/workflows/drift-check.yml` (new) | the scheduled check |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestDriftDetection`, `TestDriftCheckWorkflow` |
| `docs/operator/cost-parking.md`, `docs/operator/deploy.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestDriftDetection` (table: equal, one core behind, one connector behind, two pods of one service with old and new digests, a name missing from the registry) | the comparison exactly | report only exact mismatches and drop the "not in registry" case |
| `TestDriftCheckWorkflow` | schedule + dispatch only; environment and concurrency `production`; power-state step before anything else; the in-progress-build guard; every kubectl through `in-cluster.sh`; dispatches `catch-up.yml`, nothing else writes (no `deploy-*.sh`, no `kubectl set`) | remove the concurrency group / call deploy-core directly |
| Existing `TestInClusterScriptsUseOnlyRuntimeTools` | the inline kubectl/jq stays inside the runtime's toolset | — |

## Verify

1. Merge; the first scheduled run on the running cluster reports "at main"
   (today's deploy just rolled everything).
2. Provoke it: `kubectl rollout restart` is not drift, so instead set one
   connector's Deployment to the previous digest by hand
   (`kubectl set image … @sha256:<previous>`), run the check by dispatch:
   it lists that one service and dispatches Catch up with
   `connectors=true`; Catch up restores `:latest`; the next check is clean.
3. Stop the cluster; the next scheduled run is green with the "Stopped"
   notice and dispatches nothing.

## Risks

- **A build in progress** makes the check skip, so a drift present at the
  same time waits one more cycle. Fine.
- **Catch up restarts the whole fleet** when any connector drifted, about
  ten minutes; the connectors restore their pipelines on boot. Same as the
  button today.
- **GitHub disables scheduled workflows** after 60 days without a commit.
  Not a concern on this repo; the docs mention it anyway.

## Non-goals

Per-service restarts instead of the fleet; a Prometheus alert for drift
(the registry digest is not a metric); starting or stopping the cluster.
