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

**It happens by itself outside working hours.** The *Park* workflow
(`.github/workflows/park.yml`) is scheduled every half hour from 16:00 to
07:00 Oslo time, at :07 and :37 past the hour because GitHub drops runs at
:00 and :30 when it is busy; each run that lands in that window queues behind any
deploy or catch-up in progress and stops a Running cluster, a stopped one
is left alone. GitHub runs schedules hours late on this repository (the
first evening, a single 16:00 cron fired at 20:10 and stopped nothing),
which is why it is a window with many crons and not one o'clock: expect
the stop somewhere between 16:00 and 20:00. Still working in the evening?
Start it again; the next run stops it again. A stop from anywhere, any
hour: Actions → **Park** → *Run workflow*.

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

- **Managed disks** — 2 × 150 GB OS disks, plus every persistent volume.
- **Public IP addresses** — two Standard IPs are allocated to the node
  resource group (`20.251.107.2` for ingress, and one more).

So parking cuts the bill substantially but does not take it to zero. Deleting
the cluster would, and would also throw away the volumes and both IPs — which
means a new URL and re-issued certificates. Park it; don't delete it.

## The monthly budget

October 2026 (`plans/cost-cap.md`): a parked cluster costs about 1 450 NOK
a month in disks, load balancer, registry and addresses before a single
node-hour; two E4ds v6 nodes add 214 NOK for every day they run, and the
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
