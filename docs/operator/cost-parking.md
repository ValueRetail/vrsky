# Cost-parking the cluster

The AKS cluster can be **stopped** when nobody is using it. Compute stops
billing; the cluster keeps its identity, its disks and its IP addresses, and
comes back with the same workloads.

This is the normal state between pilots. If the production URL does not
respond and nothing is obviously broken, **check this first** — a parked
cluster looks exactly like an outage from the outside.

## Current state

```bash
az aks show -g vrsky-prod -n vrsky-prod --query powerState.code -o tsv
# Running | Stopped
```

## Stop

```bash
az aks stop -g vrsky-prod -n vrsky-prod
```

Takes a few minutes. Every pod is evicted; nothing is deleted.

**It happens by itself at 16:00.** The cluster stops itself: a CronJob in
the cluster (see [The 16:00 stop](#the-1600-stop)) asks Azure to stop it at
16:00 Oslo time, every day it is running. The nodes are gone a few minutes
later.

The *Park* workflow (`.github/workflows/park.yml`) is the backstop. It is
scheduled every half hour from 16:00 to 07:00 Oslo time, at :07 and :37
past the hour because GitHub drops runs at :00 and :30 when it is busy;
each run that lands in that window queues behind any deploy or catch-up in
progress and stops a Running cluster, a stopped one is left alone. GitHub
runs schedules hours late on this repository (2026-10-22: the first run
landed at 20:28), so Park alone means a stop somewhere between 16:00 and
21:00; it catches the evenings the CronJob did not run or failed. Still
working in the evening? Start it again; the CronJob has had its run for
the day, and Park's next run stops it again. A stop from anywhere, any
hour: Actions → **Park** → *Run workflow*.

## The 16:00 stop

`infrastructure/kubernetes/park/cronjob.yaml`: namespace `vrsky-park`,
service account `cluster-park`, CronJob `cluster-park` at `0 16 * * *`,
`timeZone: Europe/Oslo`. The job logs in with AKS workload identity as the
managed identity `vrsky-cluster-park`, reads the power state and, when it
is Running, runs `az aks stop --no-wait` (the job runs on the nodes it is
stopping, so it does not wait for them). The identity has one custom role,
*VRSky cluster park*, on the cluster alone: read it and stop it, nothing
else. `TestClusterParkCronJob` pins the schedule, the time zone, the
concurrency policy, the deadlines below and that the job runs no other
`az` command against any other cluster.

The CronJob drops a run it could not start within ten minutes of 16:00. A
cluster started the next morning would otherwise run the missed 16:00 job
straight away and stop again.

Unlike Park, the CronJob does not queue behind a deploy: GitHub's
`production` concurrency group means nothing inside the cluster. A deploy
or catch-up still rolling out at 16:00 is cut off by the stop; after the
next start the Production check sees production behind `main` and runs
Catch up, as after any stop.

Did today's run work?

```bash
kubectl get jobs -n vrsky-park
kubectl logs -n vrsky-park -l app=cluster-park --tail=20
```

### One-time setup (a human, with Owner on the subscription)

Not applied by the deploy scripts. Creating a role and assigning it needs
Owner or User Access Administrator.

```bash
SUB=8c020b54-b237-4d6e-82ba-190f6a415d1d

# 0. OIDC issuer and workload identity. Both were on on 2026-10-27. Unless
#    this prints "True  True", run the update below.
az aks show -g vrsky-prod -n vrsky-prod \
  --query '[oidcIssuerProfile.enabled, securityProfile.workloadIdentity.enabled]' -o tsv
# az aks update -g vrsky-prod -n vrsky-prod --enable-oidc-issuer --enable-workload-identity

# 1. The identity.
az identity create -g vrsky-prod -n vrsky-cluster-park -l norwayeast

# 2. Trust the CronJob's service account, and nothing else.
az identity federated-credential create -g vrsky-prod --identity-name vrsky-cluster-park \
  -n vrsky-park-cluster-park \
  --issuer "$(az aks show -g vrsky-prod -n vrsky-prod --query oidcIssuerProfile.issuerUrl -o tsv)" \
  --subject system:serviceaccount:vrsky-park:cluster-park \
  --audiences api://AzureADTokenExchange

# 3. A role that can read and stop a cluster, assigned on vrsky-prod only.
az role definition create --role-definition "{
  \"Name\": \"VRSky cluster park\",
  \"Description\": \"Read and stop the vrsky-prod AKS cluster.\",
  \"Actions\": [\"Microsoft.ContainerService/managedClusters/read\",
                \"Microsoft.ContainerService/managedClusters/stop/action\"],
  \"NotActions\": [],
  \"AssignableScopes\": [\"/subscriptions/$SUB/resourceGroups/vrsky-prod\"]
}"
# A new role takes a minute or two to be usable; retry if this says it does not exist.
az role assignment create \
  --assignee-object-id "$(az identity show -g vrsky-prod -n vrsky-cluster-park --query principalId -o tsv)" \
  --assignee-principal-type ServicePrincipal \
  --role "VRSky cluster park" \
  --scope "$(az aks show -g vrsky-prod -n vrsky-prod --query id -o tsv)"

# 4. Apply the manifest with the identity's client id filled in, against
#    the production cluster. Always apply it this way: applying the file as
#    it is in the repo puts the placeholder back and the next run fails.
az aks get-credentials -g vrsky-prod -n vrsky-prod
CLIENT_ID=$(az identity show -g vrsky-prod -n vrsky-cluster-park --query clientId -o tsv)
sed "s/REPLACE_WITH_CLUSTER_PARK_IDENTITY_CLIENT_ID/$CLIENT_ID/" \
  infrastructure/kubernetes/park/cronjob.yaml | kubectl apply -f -
kubectl get cronjob -n vrsky-park
```

The built-in *Azure Kubernetes Service Contributor Role* would also do,
but it grants `Microsoft.ContainerService/managedClusters/*`: delete,
scale, upgrade and the admin kubeconfig as well. The custom role grants
the two actions the job uses.

To try it, on an evening when the cluster may stop (it stops it):

```bash
kubectl create job --from=cronjob/cluster-park -n vrsky-park park-now
kubectl logs -n vrsky-park job/park-now -f
```

## Start

```bash
az aks start -g vrsky-prod -n vrsky-prod
```

If anything merged to `main` while it was stopped, production is behind:
the images were built, nothing was deployed. Within about half an hour the
**Drift check** notices and runs Catch up by itself; to not wait, GitHub →
Actions → **Catch up** → *Run workflow* rolls it to `main` now (see
[Deploying](deploy.md)).

No `PipelineDown` card follows a start: the rule waits until the counters
have a full hour of samples again. A polled source that is really dead
pages about an hour after the start, as it would at any other time.

Allow several minutes for the nodes to register and the workloads to become
Ready — the two-node pool has to come back before anything schedules. Check:

```bash
kubectl get nodes
kubectl get pods -A --field-selector=status.phase!=Running
```

## What survives

| | Survives a stop/start | Why it matters |
|---|:---:|---|
| Public IP `20.251.107.2` | **Yes** | Static/Standard SKU, so the URL and any DNS pointing at it stay valid |
| Persistent volumes | **Yes** | Postgres and MinIO data are on managed disks |
| TLS certificates | **Yes** | cert-manager stores them as Kubernetes secrets |
| Deployed image versions | **Yes** | Deployments are pinned by digest, so a restart cannot silently pull something newer |
| Running pods | No | All evicted; they are recreated on start |

## What you still pay for

Stopping deallocates the nodes. It does **not** stop billing for:

- **Persistent volumes** — billed by provisioned size, used or not:
  Postgres 50 GiB, Prometheus 20 GiB, NATS 3 × 8 GiB, MinIO 8 GiB,
  Grafana 5 GiB, Alertmanager 2 GiB (`TestPersistentVolumesStayWithinBudget`
  keeps the total under 120 GiB). Azure Files (`vrsky-files`) is billed by
  what is stored. The node's OS disk is ephemeral, on the VM's local NVMe:
  it does not exist, and costs nothing, while the cluster is parked.
- **Public IP addresses** — two Standard IPs are allocated to the node
  resource group (`20.251.107.2` for ingress, and one more).
- **The load balancer and the container registry.**

Until October 2026 the NATS and MinIO disks were 100 GiB each and the OS
disk a 128 GB Premium, about 790 NOK a month for a few MB of data
(`plans/disk-costs.md`). The parked floor is now about 660 NOK a month.

So parking cuts the bill substantially but does not take it to zero. Deleting
the cluster would, and would also throw away the volumes and both IPs — which
means a new URL and re-issued certificates. Park it; don't delete it.

## The monthly budget

October 2026 (`plans/cost-cap.md`): a parked cluster cost about 1 450 NOK
a month in disks, load balancer, registry and addresses before a single
node-hour (about 660 after `plans/disk-costs.md`); two E4ds v6 nodes add 214 NOK for every day they run, and the
Standard tier 21 NOK. The budget is **1 500 NOK a month** for the two
VRSky resource groups, set in two places:

- An Azure budget `vrsky-monthly` (subscription scope, filtered to the two
  resource groups) e-mails the operator at 80 and 100 percent. It stops
  nothing.
- The repository variable `MONTHLY_BUDGET_NOK` turns on the *Budget* step
  of the Production check: every half hour it reads the month-to-date
  cost of the two groups and, at or above the budget, **stops the cluster
  and opens the issue**; a start afterwards lasts until the next run that
  month. A failed cost query fails the run and stops nothing. The deploy
  identity needs *Cost Management Reader* on the two resource groups for
  this (an Owner grants it); without the variable the step is skipped.

What the budget assumes: one node, Free tier, a start by hand around
09:00 on working days and the 16:00 stop. Two nodes and the Standard tier
would spend it by the 12th.

## Verifying after a start

```bash
kubectl get pods -n vrsky-platform
curl -fsS https://20.251.107.2.sslip.io/healthz
```

If the health check fails but pods are Ready, the problem is more likely DNS
interception of the `sslip.io` host than the cluster — see
[Troubleshooting](troubleshooting.md#https-fails-with-a-certificate-error-or-a-webhook-never-arrives).
