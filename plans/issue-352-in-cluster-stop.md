# Stop the cluster at 16:00 from inside the cluster (#352)

## Open questions

None blocking. Decisions taken without asking are listed under Approach
and repeated in the PR.

## Goal

The cluster stops at 16:00 Oslo time, on time, every day it is running.
GitHub fires only five or six of Park's 32 daily runs and the first lands
hours late (2026-10-22: 20:28 Oslo). Kubernetes runs CronJobs on time, and
the stop only matters while the cluster runs, so the cluster stops itself.
Park stays as the backstop.

## Acceptance criteria (from the issue)

- A CronJob (new manifest under `infrastructure/kubernetes/`) with
  `schedule: "0 16 * * *"`, `timeZone: Europe/Oslo` and
  `concurrencyPolicy: Forbid` runs `az aks stop` for `vrsky-prod` when the
  cluster is Running.
- It authenticates with AKS workload identity through a user-assigned
  managed identity whose only rights are on the `vrsky-prod` cluster. The
  one-time setup (identity, federated credential, role assignment, enabling
  OIDC issuer and workload identity if they're off) is written down as
  commands for a human to run.
- Park (`.github/workflows/park.yml`) stays as the backstop.
- A guard test pins the schedule, the time zone, the concurrency policy,
  and that the job runs no `az` verb other than `aks show` and `aks stop`
  (plus `az login`, which the workload-identity login needs).
- `docs/operator/cost-parking.md` says how the stop works now.

Added here:

- The service account carries the `azure.workload.identity/client-id`
  annotation and the pod template the `azure.workload.identity/use: "true"`
  label.
- The manifest is counted by `TestCPURequestsStayWithinBudget`, since it
  runs in production (applied by hand, once).

## Files

| File | Change |
|---|---|
| `infrastructure/kubernetes/park/cronjob.yaml` (new) | namespace `vrsky-park`, service account `cluster-park`, CronJob `cluster-park` |
| `src/pkg/managementapi/nodeconfig_test.go` | new guard test; the CPU-budget test reads CronJobs and lists the new manifest |
| `docs/operator/cost-parking.md` | how the stop works now; the one-time setup |
| `plans/backlog.md` | strike item 1, add #352 |
| `plans/issue-352-in-cluster-stop.md` (this file) | |

Not touched: `.github/workflows/park.yml`, the deploy scripts, anything in
Azure or the cluster.

## Approach

- One manifest, three objects: a namespace of its own (nothing else runs
  there, so the federated credential trusts one service account in one
  namespace), the service account with the client-id annotation, and the
  CronJob.
- The job: `az login --service-principal` with the federated token the
  webhook mounts (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`,
  `AZURE_FEDERATED_TOKEN_FILE` are injected); `az aks show`
  reads `powerState.code`; only on `Running` it runs `az aks stop
  --no-wait`. Every `az aks` call names the subscription explicitly.
- `--no-wait`: the job runs on the nodes it is stopping. Without it the pod
  would be killed mid-call and the Job could be left unfinished across the
  stop.
- `startingDeadlineSeconds: 600`. Without it a start the next morning (or
  after a weekend stopped) makes the CronJob controller run the missed
  16:00 job at once, which would stop the cluster at 08:00. Ten minutes
  after 16:00 the run is dropped instead.
- `activeDeadlineSeconds: 600`, `backoffLimit: 2`: a run that is still
  around when the cluster comes back is past its deadline and is failed,
  not retried.
- Image `mcr.microsoft.com/azure-cli:2.91.0` (built 2026-09-29; the newest
  tag, four weeks old). Runs as uid 1000 with a read-only root filesystem;
  `AZURE_CONFIG_DIR` and `HOME` point at an `emptyDir`.
- Requests 10m CPU / 64Mi memory (az is a short Python run), limits
  500m / 512Mi; `restartPolicy: Never`; history limits 3 and 3.
- Role: a custom role "VRSky cluster park" with only
  `Microsoft.ContainerService/managedClusters/read` and
  `.../stop/action`, assignable in the `vrsky-prod` resource group, assigned
  at the cluster's scope. The built-in "Azure Kubernetes Service
  Contributor Role" was checked (read-only) and grants
  `Microsoft.ContainerService/managedClusters/*`, which includes delete,
  write (scale, upgrade, tier) and the admin kubeconfig; far more than a
  stop needs.
- The read-only check on 2026-10-27 shows the OIDC issuer and workload
  identity are on, so the doc gives the enable command only as a
  conditional step.
- Not added to the deploy scripts: the client id is a placeholder until a
  human creates the identity, and deploy config is not an agent's to
  change. A human applies the manifest once, after the setup.

## Risks

- A stop at 16:00 while someone works: same as Park, start it again.
  Park still stops an evening start at its next run.
- The job fails silently (bad client id, role missing): Park still stops
  the cluster later that evening, as today. The doc says how to check the
  last run (`kubectl get jobs -n vrsky-park`).
- Image pull from MCR at 16:00 fails: same fallback.
- `az login` for an identity whose only role is a two-action custom role
  on the cluster. The deploy identity logs in with roles below
  subscription scope (Contributor on the cluster and the registry), and
  any assignment in a subscription should make it visible to the login,
  but this exact case is untested until the first manual run. If it says
  "No subscriptions found", the run fails and Park takes over.
- The stop does not queue behind a deploy the way Park does (GitHub's
  `production` concurrency group means nothing in the cluster). A rollout
  in progress at 16:00 is cut off; after the next start the Production
  check runs Catch up. Said in the doc.
- The CronJob controller after a start: covered by
  `startingDeadlineSeconds` (see above). Not verifiable without running it
  in the cluster.

## Verify

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race -count=1 ./pkg/managementapi/...` from `src/`.
- Mutation: break each pinned property in the manifest once (schedule,
  time zone, concurrency policy, an extra `az` verb, the client-id
  annotation, the pod label, the manifest missing from the CPU list) and
  see the guard fail; restore.
- Locally: the pinned image starts as uid 1000 with a read-only root and
  `az version` runs (docker, no Azure call).
- After merge, by a human: the setup commands, `kubectl apply`, then a
  manual run (`kubectl create job --from=cronjob/cluster-park -n vrsky-park
  park-test`) on an evening when a stop is wanted, and the cluster is
  Stopped a few minutes later.
