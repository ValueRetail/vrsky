# Auto-deploy while the cluster is parked

Written 2026-10-19, with the cluster stopped and `plans/auto-deploy.md`
in production since this morning.

## The gap

`az aks stop` is now the normal state between working sessions
(`docs/operator/cost-parking.md`; ~$22/day saved). A merge that touches
code while the cluster is stopped builds its images fine and then fails
the deploy job at the first in-cluster command: a red run, a Teams card
from the *CI failure alert* workflow, and a manual `gh run rerun --failed`
after the next `az aks start`. Merges that deploy nothing are unaffected.

## Open questions

1. **A stopped cluster: green or red?** Recommended: **green**, with a
   notice annotation and a job summary saying *cluster stopped, nothing
   deployed, N images are waiting in the registry*. Red for a state you
   chose on purpose is noise, and the standing rule is that red means
   something to fix. Alternative: a distinct `neutral`/skipped outcome;
   GitHub has no such job state, so it would be green anyway or red.
2. **Catching up after `az aks start`.** Recommended: a **manual
   `Catch up` workflow** (`workflow_dispatch`): logs in, refuses if the
   cluster is stopped, rolls every core service by its current `:latest`
   digest (a no-op for anything already there), restarts the connector
   fleet, applies monitoring, runs the smoke check. One click after a
   start, no rebuild (the images are already in ACR). Alternative:
   nothing automatic and a re-run of the last red run's failed jobs — that
   stops working once the stopped-cluster run is green, hence the button.
3. **Should the catch-up restart all connectors?** Recommended: **yes**;
   they pin `:latest`, so only a restart picks up a new image, they
   restore their pipelines on boot (#306), and the fleet restart takes
   ~10 minutes in-cluster. Alternative: core only by default, connectors
   on request — then a connector change merged while parked is easy to
   forget.

## Goal

Merging while the cluster is parked is uneventful: images build, the run
is green and says what is waiting. After `az aks start`, one click brings
production up to `main`. The deploy job never fails for a state you chose.

## What exists (checked)

- `deploy` job (`build-push.yml`): login → *Stage the deploy files* →
  core / connectors / monitoring → *Smoke check*, each through
  `in-cluster.sh`. With the cluster stopped, `az aks command invoke`
  fails on the first step.
- The service principal holds `Contributor` on the cluster, which covers
  `az aks show` (`powerState.code`: `Running` | `Stopped`).
- `cost-parking.md` documents stop/start and what survives; it does not
  mention the deploy pipeline yet.

## Approach

### 1. The deploy job checks the power state first

A step right after login:

```yaml
- name: Cluster power state
  id: cluster
  run: |
    state=$(az aks show -g "$AKS_RESOURCE_GROUP" -n vrsky-prod --query powerState.code -o tsv)
    echo "state=$state" >> "$GITHUB_OUTPUT"
    if [ "$state" != Running ]; then
      echo "::notice title=Cluster is $state::Nothing deployed. The images for this merge are in the registry; after az aks start, run the Catch up workflow."
      { echo "## Cluster is $state"; echo; echo "Nothing deployed. Waiting in the registry: \`${{ needs.plan.outputs.images }}\`."; echo; echo "After \`az aks start\`: Actions → **Catch up** → Run workflow."; } >> "$GITHUB_STEP_SUMMARY"
    fi
```

Every later step gets `if: steps.cluster.outputs.state == 'Running'`
(the staging step too; it resolves digests, harmless but pointless).
The job ends green.

### 2. `Catch up` workflow (`.github/workflows/catch-up.yml`)

`workflow_dispatch`, one optional input `connectors` (default `true`).
Jobs: a single `catch-up` job on the `production` environment and the
`production` concurrency group (so it queues behind a deploy, never
interleaves), OIDC login, refuse with a clear error if the cluster is not
`Running`, then the same steps as the deploy job's tail: stage, resolve
every core digest, `deploy-core-azure.sh` for all core services,
`deploy-connectors-azure.sh` (full fleet) when `connectors` is true,
`install-monitoring.sh`, `smoke-prod.sh` with the three in-use consumers.
Nothing is rebuilt.

The two jobs share their steps by construction, not by copy: the deploy
job's tail and the catch-up job call the same scripts with the same
`in-cluster.sh`; the guard below keeps both honest.

### 3. Docs

- `docs/operator/deploy.md`: a section *While the cluster is parked*
  (merges build, the run is green and says so; one click after start).
- `docs/operator/cost-parking.md`: under *Start*, "if anything merged
  while it was stopped: Actions → Catch up".

## Files

| File | Change |
|---|---|
| `.github/workflows/build-push.yml` | `Cluster power state` step; `if:` on the later deploy steps |
| `.github/workflows/catch-up.yml` (new) | the manual catch-up |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestDeployJobSkipsWhenClusterStopped`, `TestCatchUpWorkflow` |
| `docs/operator/deploy.md`, `docs/operator/cost-parking.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestDeployJobSkipsWhenClusterStopped` | the deploy job has a power-state step before any in-cluster step, and every in-cluster step is gated on `steps.cluster.outputs.state == 'Running'` | drop the `if` from one step |
| `TestCatchUpWorkflow` | dispatch-only; environment and concurrency `production`; refuses when not `Running`; every kubectl/helm goes through `in-cluster.sh`; calls `deploy-core-azure.sh`, `deploy-connectors-azure.sh`, `install-monitoring.sh`, `smoke-prod.sh` | remove the concurrency group / call kubectl directly |
| Existing `TestDeployJobGatesOnTests`, `TestGoJobsRetryModuleDownloads` | unchanged gates | — |

## Verify

1. Merge this PR **while the cluster is stopped**: images build, the
   deploy job is green with the notice and summary, nothing touched.
2. `az aks start`, then Actions → Catch up → Run workflow: core rolls to
   the waiting digests, the fleet restarts and restores its pipelines,
   smoke ok. Production is at `main`.
3. Run Catch up again with the cluster stopped: a clear refusal, red
   (that one *is* a mistake).

## Risks

- **Forgetting the catch-up** after a start leaves production one or more
  merges behind, silently. The start doc says so, and the stopped-run
  summary says so; a later improvement could compare running digests with
  the registry and alert. Not now.
- **A deploy and a catch-up at once** queue on the concurrency group.

## Non-goals

Starting the cluster from a workflow; stopping it on a schedule; a
digest-drift alert; a TEST environment.
