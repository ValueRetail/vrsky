# file-consumer: one pod at a time during a deploy (#425)

Backlog item 40. `vrsky-file-consumer` runs one replica with the default
RollingUpdate strategy (maxSurge 25% rounds up to 1, maxUnavailable 25%
rounds down to 0). On every restart the new pod starts, restores every
running file pipeline (`sdk.RestoreRunning` → `startConnection`) and polls
the shared `vrsky-files` volume while the old pod is still polling it. A
file that settles in that window can be published by both. #391 made each
watcher re-read the saved done set once per poll before publishing
(`mergeSaved` in `src/cmd/file-consumer/known.go`), which shrinks the window
to "both pods decide in the same poll", but doesn't close it.

## Acceptance criteria

1. The generated connector manifest gives `vrsky-file-consumer`
   `strategy: {type: Recreate}`. No other connector Deployment changes.
2. The change is in the generator (`deploy-connectors-azure.sh`), not only
   in `connectors.yaml`. The deploy job regenerates the manifest from the
   script before applying it, so a hand edit to the YAML alone would be
   overwritten at deploy time and never reach the cluster.
3. The switch lands on a cluster where the Deployment is still
   RollingUpdate. A plain client-side `kubectl apply` handles that on its
   own: `DeploymentSpec.strategy` merges with `retainKeys`, so the apply
   patch is `{"$retainKeys":["type"],"type":"Recreate"}` and the
   server-defaulted `rollingUpdate` block goes with it (covered by
   kubectl's test-cmd). Grafana's "rollingUpdate: Forbidden" (#320,
   kubernetes/kubernetes#24198) came through helm. As a harmless
   safeguard the deploy script still patches the Deployment first, the
   same way `install-monitoring.sh` does for Grafana. A no-op once the
   Deployment is Recreate, and on a fresh cluster.
4. A guard test next to the other manifest guards
   (`src/pkg/managementapi/`) fails when file-consumer is not Recreate in
   the committed manifest or in a fresh render of the script, when the two
   differ, or when the script stops dropping `rollingUpdate` before its
   apply. Run with `-count=1`, plus a mutation run.
5. Operator docs say file-consumer restarts stop-then-start; the file
   connector page says uploads fail for those seconds. Backlog item 40 is
   struck with (#425); the related flaws found below go in as a new item.

## Files

| File | Change |
|---|---|
| `infrastructure/azure/deploy-connectors-azure.sh` | `strategy: Recreate` for file-consumer in `emit`; drop `rollingUpdate` before the apply |
| `infrastructure/kubernetes/connectors/connectors.yaml` | regenerated (`GENERATE_ONLY=1`) |
| `src/pkg/managementapi/connector_strategy_test.go` (new) | the guard |
| `docs/operator/deploy.md`, `docs/connectors/file.md` | one paragraph each |
| `plans/backlog.md` | strike 40, add the sftp/cloud-storage item |

## Trade-off: a short gap with no file-consumer

Recreate scales the old ReplicaSet to zero, waits until its pod is gone,
then starts the new one. For that time nothing watches the folders and
`/upload` has no endpoint behind its Service.

- Gap length: the old pod's graceful stop (SIGTERM, up to the default 30 s
  grace), image pull (`imagePullPolicy: Always`, mostly a digest check),
  start, restore, then two polls 5 s apart before a file counts as
  settled. Tens of seconds in practice.
- Files that land in the gap are delayed, not lost. From the code:
  - On stop, a poll that sees `ctx.Err()` breaks out and a publish cut off
    by the stop rolls the name back out of the done set
    (`pollOnce`, service.go), so it is not saved as done.
  - On start, `loadKnown` (known.go) with a saved checkpoint takes as done
    only names in the saved set that are still in the folder. Everything
    else (files that landed in the gap) is not known, and `pollOnce`
    publishes it once it has been seen unchanged across two polls. The
    docs already promise this for "a file that landed in the last seconds
    before a stop, restart or deploy" (`docs/connectors/file.md`).
  - Exceptions: a pipeline's first ever start takes every file in the
    folder as done, and a failed checkpoint read at start (DB error) does
    the same (logged at Error). The second one is worse under Recreate:
    under RollingUpdate the old pod was still polling and published a
    file that landed in the gap, but now a file that lands in the gap
    while the new pod's checkpoint read fails is marked done and never
    published. Rare (a DB error at exactly that start) and logged.
- Uploads through the editor fail with a proxy error for those seconds;
  retrying works. Rare (deploys only) and visible to the user, unlike a
  duplicate publish.

A per-connection lease would keep the rolling update, but it is a code
change across the watcher and the checkpoint table for a few seconds per
deploy. Recreate is what Grafana and alert-responder already use for the
same "never two at once" reason.

## What merging does on the next deploy

`affected.sh` on this PR's paths prints one target, `connectors-manifest`
(the script and the YAML match `infrastructure/azure/deploy-connectors-azure.sh|infrastructure/kubernetes/connectors/*`;
the `_test.go`, docs and plans add nothing). No images are built.

The deploy job, with the cluster running, then runs the full
`deploy-connectors-azure.sh` in-cluster:

1. regenerates `connectors.yaml` from the script,
2. creates the `worker-file-api` secret if missing (it exists),
3. client-side dry run,
4. new, a safeguard: finds `vrsky-file-consumer` on RollingUpdate and
   patches it to Recreate with `rollingUpdate: null` (no pod restart: the
   pod template doesn't change; the apply alone would do the same),
5. `kubectl apply` (only file-consumer's strategy differs),
6. `kubectl rollout restart` on all 31 connector Deployments, then waits
   for each. So yes: every connector restarts, file-consumer included, and
   file-consumer's restart is already a Recreate one.
7. Smoke check on webhook-consumer, business-central-consumer and
   remote-agent.

While the cluster is parked the deploy job builds nothing (no images),
deploys nothing, and is green. After `az aks start` the Production check
compares image digests only. This PR changes no image, so the check will
not dispatch Catch up because of it. The Recreate switch reaches the
cluster when someone runs Catch up with "connectors" on, when the check
dispatches it for some other connector's drifted image, or with the next
merge that touches the connector manifest.

## Other connectors checked

| Connector | Same flaw? | Why |
|---|---|---|
| file-producer | No | Reads from a JetStream pull durable through the SDK runner. Two pods split messages, they don't both get them. Its `/files` API is stateless reads and deletes on the share. |
| sftp-consumer, cloud-storage-consumer | Related, not the same | They poll a remote folder or bucket, so two pods overlap the same way. But their done set is in memory only (`processed` in `runPoller`), so with `after_action: none` every restart republishes every file still there, Recreate or not. Recreate alone wouldn't fix them; they need a saved done set like #391's. New backlog item. |
| other polling consumers (api, db, salesforce, retail ERPs, tenant, remote-agent) | Not checked in depth | They poll APIs or cursors, not a shared folder. Any duplicate there depends on each one's cursor handling; out of scope here and noted in the same backlog item. |
| kafka, rabbitmq, webhook consumers | No | Broker group/queue semantics or inbound HTTP. |

## Risks

- The pre-apply patch is a kubectl write in a production script. It is
  the same command `install-monitoring.sh` runs for Grafana, guarded the
  same way. If it failed, `set -e` stops the script before the apply and
  the restart, the deploy goes red, and the fleet keeps running as it was.
- The fleet-wide restart on merge is what every manifest change does
  today; about ten minutes and every pipeline restores on boot.

## Verification

- `GENERATE_ONLY=1 bash infrastructure/azure/deploy-connectors-azure.sh`
  and `git diff` shows only the file-consumer strategy.
- `go test -count=1 -run TestFileConsumerRecreates ./pkg/managementapi/`;
  mutation: drop the strategy from the script, hand-edit only the YAML,
  remove the patch step, each must fail.
- From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -count=1 ./pkg/managementapi/`.
