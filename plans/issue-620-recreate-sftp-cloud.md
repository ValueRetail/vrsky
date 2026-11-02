# sftp-consumer and cloud-storage-consumer: one pod at a time (#620)

Backlog item 106. Since #538 both consumers keep their done set in the
connection checkpoint, so a restart no longer republishes everything. A
rolling deploy still runs the old and new pod side by side for a few
seconds, and with `after_action: delete` or `move` both can list and
publish the same file before either removes it. This is #425's overlap.
`vrsky-file-consumer` already uses `strategy: Recreate`; do the same for
these two.

## Acceptance criteria

1. The generated connector manifest gives `vrsky-sftp-consumer` and
   `vrsky-cloud-storage-consumer` `strategy: {type: Recreate}`, with no
   `rollingUpdate` block. No other Deployment changes.
2. The change is in the generator (`deploy-connectors-azure.sh`), not only
   in `connectors.yaml`: the deploy regenerates the manifest from the script.
3. The script calls `drop_rolling_update` for both before its
   `kubectl apply`, as it does for file-consumer.
4. The guard test covers all three consumers (a list, so a fourth is one
   line), and fails on the unmodified script.
5. Operator docs and the two connector pages say the old pod stops first.
   The operator doc says how the change reaches the cluster.
6. Backlog item 106 is struck with (#620).

## Files

| File | Change |
|---|---|
| `infrastructure/azure/deploy-connectors-azure.sh` | Recreate for the three consumers in `emit`; two more `drop_rolling_update` calls |
| `infrastructure/kubernetes/connectors/connectors.yaml` | regenerated with `GENERATE_ONLY=1` (it exits before any kubectl call) |
| `src/pkg/managementapi/connector_strategy_test.go` | `TestFileConsumerRecreates` becomes `TestPollingConsumersRecreate`, looping over `recreateConsumers` |
| `docs/operator/deploy.md`, `docs/connectors/sftp.md`, `docs/connectors/cloud-storage.md` | the gap, and the operator step |
| `plans/backlog.md` | strike 106 |

## Trade-off: a short gap

Recreate stops the old pod, then starts the new one: tens of seconds with
nothing listing the remote folder or bucket. Nothing is lost:

- Both consumers publish a name only after it is not in the saved done set,
  and a publish cut off by the stop is not saved as done, so files that
  land in the gap go out on the new pod's first polls.
- Unlike the file consumer (#425), a failed checkpoint read at start makes
  these two publish nothing and retry every poll (docs/connectors/sftp.md,
  "Checkpoint unreadable"), so the "file skipped when the read fails" case
  #425 documented does not exist here.
- Event-mode cloud storage reads from a queue, which keeps messages while
  the pod is away.
- Neither has an HTTP endpoint that users call during a deploy, so there is
  no equivalent of file-consumer's failing `/upload`. (The editor's
  Test connection and preview calls fail for the same seconds; retrying
  works.)

## What merging does

The merge touches the script and `connectors.yaml`, so `affected.sh`
selects `connectors-manifest`. With the cluster running, the deploy job
runs the full `deploy-connectors-azure.sh`: regenerate, dry run, the
`drop_rolling_update` patches (no restart, the pod template is unchanged),
`kubectl apply`, then a rollout restart of every connector, which for these
two is already a Recreate one.

With the cluster parked, the deploy job skips the rollout and stays green.
The Production check compares image digests only, so it will not roll this
change. It reaches the cluster with Actions, Catch up, *connectors* ticked
after the next start, or with the next merge that touches the connector
manifest. Image-only deploys do not apply the manifest.

## Risks

- Hard-limit area (Kubernetes manifest): PR with `agent:needs-human`, no
  merge by the agent.
- The pre-apply patch is a kubectl write in a production script; it is the
  same command already run for file-consumer. If it fails, `set -e` stops
  the script before the apply.
- A few seconds more downtime per deploy for these two consumers.

## Verification

- `GENERATE_ONLY=1 bash infrastructure/azure/deploy-connectors-azure.sh`:
  `git diff` shows only the two strategy blocks.
- The extended test fails on the unmodified script (four errors: two
  Deployments in the rendered and committed manifest), passes after.
  Mutation: remove one `drop_rolling_update` call, and hand-edit only the
  YAML; each must fail.
- From `src/`: `go build ./...`, `go vet ./...`,
  `go test -count=1 ./pkg/managementapi/ -run 'Recreate|Strategy|NodeConfig'`,
  `go test -race ./pkg/managementapi/...`, `golangci-lint run ./...`.
