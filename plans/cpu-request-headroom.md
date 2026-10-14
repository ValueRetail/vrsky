# CPU request headroom: right-size, don't buy

Measured on 2026-10-14 against `vrsky-prod` (two `Standard_E4ds_v6`,
norwayeast).

## The numbers

| | Node 0 | Node 1 | Cluster |
|---|---|---|---|
| Allocatable CPU | 3860m | 3860m | 7720m |
| **Requested** | 3486m (**90%**) | 2769m (71%) | 6255m (81%) |
| **Used, 7-day average** | ~75m | ~75m | **150m (2%)** |
| Memory requested / used | 27% / 13% | 18% / 12% | fine |

Nothing is slow. The cluster is 98% idle by use and 81% full by
reservation, because requests were set by guess when each piece arrived.
The busiest VRSky workload over seven days peaked at 39m (Prometheus); the
management API at 31m; every connector at 2m. A third node costs
**$0.468/h ≈ $342/month** (about 3,500 NOK) and would carry no load.

Where the reservation goes (requests × replicas, 7-day peak use in brackets):

| Workload | Requested | Peak used | Defined in |
|---|---|---|---|
| nats-platform ×3 | 750m | 20m | `platform-nats/statefulset.yaml:77` |
| ama-logs (Container Insights, AKS add-on) | 340m + 170m | 11m | Azure |
| metrics-server (AKS) | 312m | 13m | Azure |
| prometheus | 300m | 39m | `monitoring/prometheus-values.azure.yaml:14` |
| postgresql | 250m | 6m | `postgresql/statefulset.yaml:68` |
| minio | 250m | 1m | `minio/deployment.yaml:74` |
| gatekeeper (Azure Policy add-on, dry-run only) | 300m | 53m | Azure |
| coredns (AKS) | 200m | 6m | Azure |
| management-api, ui, data-filter, data-converter ×2 each | 800m | ≤31m | the four `deployment.yaml`s |
| 31 connectors, 44 pods at 25m | ~1100m | ≤2m each | `deploy-connectors-azure.sh:315` |
| grafana | 100m | 2m | `prometheus-values.azure.yaml:95` |

## Open questions

1. **Right-size instead of a third node.** Recommended: **yes.** Saves
   $342/month and gets node 0 from 90% to about 60%. The alternative, a
   third node, is a one-line `az aks nodepool scale` and is the right
   answer only once real load shows up, which the new alert (below) will
   say.
2. **Restart Postgres for 150m?** Lowering its request restarts the single
   database pod: 20–30 s where the API answers 503 and every connector
   reconnects. Recommended: **leave Postgres at 250m**; it is not where the
   headroom is. MinIO (250m → 50m, a 10 s restart, payload spill only) is
   included.
3. **Azure Policy add-on.** Its two constraints are in *dry-run* (one
   flags the HTTP-only webhook ingress, which is deliberate) and it
   reserves 360m. Recommended: **leave it for now**; disabling an Azure
   add-on is a separate decision. Container Insights ingests 3 GB a month
   (~$8) and backs the Azure Monitor node alert you want; leave it.
4. **An alert for next time**: `NodeCPURequestsHigh` when a node's
   requests exceed 85% of allocatable for an hour, `warning`, platform
   target. Recommended: yes, so this is noticed before scheduling fails.

## What the build found (2026-10-14)

- The alert's expression evaluated live gives node 0 **0.877** today, so
  the rules must be applied **after** the right-sizing, or the first thing
  the new alert does is page about the state it was written to prevent.
- The budget guard counts the explicit list of manifests production runs
  (`deploy-vrsky-platform.sh`, `deploy-azure.sh`,
  `deploy-connectors-azure.sh`); the rest of `infrastructure/kubernetes`
  is lab-only or created on demand (per-tenant NATS template, Loki,
  promtail, KES, MinIO's 4-replica layout, Traefik) and skewed a
  directory-wide sum by 1100m. After the change the counted set is
  **1570m of the 2000m budget**; NATS back at 250m makes it 2170m and
  fails.
- Grafana's request lives in `grafana-values.azure.yaml` (its own chart),
  not the Prometheus values.

## Goal

Requests reflect what runs: at least 2× the 7-day peak, never below 10m.
Node 0 ends near 60%, the cluster near 50%, with the same limits (bursts
are unchanged) and no new node.

## Approach

### 1. New requests (limits unchanged)

| Workload | Request now | New | Saves |
|---|---|---|---|
| nats-platform ×3 | 250m | **50m** | 600m |
| prometheus | 300m | **100m** | 200m |
| minio | 250m | **50m** | 200m |
| data-filter ×2, data-converter ×2 | 100m | **50m** | 200m |
| management-api ×2 | 100m | **50m** | 100m |
| ui ×2 (static nginx) | 100m | **25m** | 150m |
| grafana | 100m | **50m** | 50m |
| 31 connectors (44 pods) | 25m | **10m** | 660m |
| **Total** | | | **~2160m** |

After: cluster ~4100m of 7720m (53%); node 0 about 2400m (62%).
Postgres and the AKS add-ons untouched.

### 2. Where

- Repo: the manifests and values above; `deploy-connectors-azure.sh`
  (`cpu: 25m` → `10m`), regenerate `connectors.yaml` with `GENERATE_ONLY=1`.
- Prod, on your word, **requests first, rules last**: `kubectl set
  resources` per Deployment/StatefulSet for the core pieces (the manifests are not applied directly, as before);
  `deploy-connectors-azure.sh` for the fleet (it applies its manifest and
  the connectors restore their pipelines on restart, #306);
  `install-monitoring.sh` for Prometheus and Grafana. NATS rolls one
  replica at a time; JetStream stays up.

### 3. The alert

```yaml
- alert: NodeCPURequestsHigh
  expr: |
    sum by (node) (kube_pod_container_resource_requests{resource="cpu"})
      / on (node) kube_node_status_allocatable{resource="cpu"} > 0.85
  for: 1h
  labels: { severity: warning }
```

with a promtool unit test (a node at 90% fires, one at 70% does not).

### 4. A guard in the repo

`TestCPURequestsStayWithinBudget` (beside the other manifest guards):
sums the CPU requests of VRSky-owned workloads from the repo's manifests
and values (replicas included) and fails above **3000m**, naming the
biggest items — so the next "250m by default" is caught in review rather
than on the node.

## Files

| File | Change |
|---|---|
| `infrastructure/kubernetes/platform-nats/statefulset.yaml` | 250m → 50m |
| `infrastructure/kubernetes/minio/deployment.yaml` (+ `statefulset-distributed.yaml` for parity) | 250m → 50m |
| `infrastructure/kubernetes/{management-api,ui,data-filter,data-converter}/deployment.yaml` | as the table |
| `infrastructure/kubernetes/monitoring/prometheus-values.azure.yaml` | prometheus 100m, grafana 50m |
| `infrastructure/azure/deploy-connectors-azure.sh`, `infrastructure/kubernetes/connectors/connectors.yaml` | 25m → 10m |
| `infrastructure/prometheus-rules.yml`, `prometheus-rules-test.yml` | `NodeCPURequestsHigh` |
| `src/pkg/managementapi/nodeconfig_test.go` | the budget guard |
| `docs/operator/cost-parking.md` or `monitoring.md` | a short "requests vs use" note and the alert |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestCPURequestsStayWithinBudget` | the repo's requests sum under 3000m | set nats back to 250m |
| promtool: `NodeCPURequestsHigh` | fires at 90% for an hour, not at 70% | drop the `for` / flip the threshold |
| existing manifest guards | nothing else moved | — |

## Verify in prod (on your word)

1. `kubectl describe nodes`: node 0 ≤ 65% requested, node 1 ≤ 55%.
2. All pods Running; the three running pipelines restored (webhook 401,
   Business Central poller, remote agent).
3. No new throttling: `container_cpu_cfs_throttled_periods_total` rates
   unchanged (limits did not move).
4. Prometheus: `NodeCPURequestsHigh` inactive.

## Risks

- **A request below real need** only changes scheduling priority under
  contention, not what a pod may use; limits are what cap it and they are
  untouched. At 2% use there is no contention.
- **Rolling restarts**: NATS one at a time, MinIO ~10 s, connectors
  self-restore. Postgres deliberately not restarted.
- **AKS add-ons keep their 1.4 core** of reservation; that is Azure's
  floor and the reason the cluster cannot go below ~30%.

## Non-goals

A third node; the cluster autoscaler; a VPA; memory requests (fine);
disabling Container Insights or Azure Policy.
