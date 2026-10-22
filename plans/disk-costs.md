# Disk costs: the floor a parked cluster still pays

Measured 2026-10-22 on `vrsky-prod` (one Standard_E4ds_v6 node, Free tier,
parked 16:00–07:00). Prices: Azure retail, Norway East, NOK per month.

## The numbers

| Disk | Size, type | Used | NOK/month | Billed while parked |
|---|---|---|---|---|
| Node OS disk | 128 GB Premium (P10) | — | ~227 | yes |
| NATS `jetstream-nats-platform-0/1/2` | 3 × 100 GiB StandardSSD ZRS (E10) | 2 MB, 2 MB, 25 MB | ~452 | yes |
| MinIO `minio-data` | 100 GiB StandardSSD ZRS (E10) | 2 MB | ~151 | yes |
| Postgres | 50 GiB E6 ZRS | 68 MB | ~75 | yes |
| Prometheus | 20 GiB E4 ZRS | ≤ 18 GB by `retentionSize` | ~38 | yes |

Why the NATS disks are 100 GiB: three **KV buckets** (`message_state`
10 GiB, `retry_queue` 50 GiB, `integration_locks` 1 GiB, each R3) reserve
61 GiB on every server against `max_file: 90G`. They were created on
2026-08-10 by `platform-nats/kv-setup-job.yaml` (applied by
`deploy-vrsky-platform.sh`), are empty, and **no Go code reads or writes
them** (checked: no reference to the bucket names or to `KeyValue` under
`src/`). The streams that are used are small: `VRSKY_DATA` R1, capped at
512 MiB (20 MB today); `VRSKY_DLQ` R1, age-limited (0 bytes today).

The node size supports an **ephemeral OS disk on its local NVMe**
(`EphemeralOSDiskSupported: True`, placement `NvmeDisk`, 220 GiB). An
ephemeral OS disk costs nothing and does not exist while the cluster is
parked; the managed P10 is billed around the clock.

## Open questions

1. **Delete the three KV buckets** and stop `deploy-vrsky-platform.sh`
   from creating them? Recommended: **yes**; nothing uses them and they
   are why the disks are 100 GiB. The job file stays deleted, not
   commented out; the README line about it goes too.
2. **New sizes.** Recommended: **NATS 3 × 8 GiB** with `max_file: 6G`
   (12× today's cap on `VRSKY_DATA`), **MinIO 8 GiB** (spill objects
   expire after a day; 2 MB today). Same storage class (`managed-csi`,
   StandardSSD ZRS), so nothing else changes. **Postgres and Prometheus
   stay** (shrinking Postgres means a dump and restore of real data for
   ~65 NOK/month; Prometheus is already small).
3. **No message lost during the NATS swap.** Recommended: raise
   `VRSKY_DATA` and `VRSKY_DLQ` to R3 for the swap, replace one NATS
   replica at a time (each new one catches up from the other two), then
   set them back to R1. Alternative: accept losing undelivered messages
   in a quiet window, which is simpler and wrong for a platform whose
   promise is delivery.
4. **OS disk fallback.** If AKS refuses ephemeral OS on NVMe placement
   for this size, the fallback is a **64 GB StandardSSD managed OS disk**
   (~50 NOK instead of ~227). Recommended: try ephemeral first; the
   attempt is one `az aks nodepool add` that either succeeds or errors.
5. **Order.** Recommended: **OS disk, then NATS, then MinIO**, each a
   separate step on your "apply it", each verified (pods Running, smoke
   ok, pipelines restored) before the next. One PR carries all repo
   changes.

## Saving

| Step | Before | After | Saves / month |
|---|---|---|---|
| OS disk ephemeral | ~227 | 0 | ~227 |
| NATS 3 × 8 GiB E2 ZRS | ~452 | ~28 | ~424 |
| MinIO 8 GiB E2 ZRS | ~151 | ~9 | ~141 |
| **Total** | | | **~790** |

That takes the parked floor from about 1 450 to about 660 NOK a month.

## Approach

### 1. OS disk (new system pool, then remove the old one)

```bash
az aks nodepool add -g vrsky-prod --cluster-name vrsky-prod -n epool \
  --mode System --node-vm-size Standard_E4ds_v6 --node-count 1 \
  --node-osdisk-type Ephemeral --node-osdisk-size 128   # placement NvmeDisk; fallback in Q4
kubectl cordon / drain the fpool node   # PDBs allow it (checked 2026-10-20)
az aks nodepool delete -g vrsky-prod --cluster-name vrsky-prod -n fpool
```

Two nodes for about half an hour (~5 NOK). Persistent volumes are ZRS and
follow their pods to the new node. Verify a stop/start cycle with the
ephemeral pool before calling it done (AKS recreates nodes on start;
observed 2026-10-22: the node name changed across a start).

### 2. NATS (one replica at a time)

1. `nats stream edit VRSKY_DATA --replicas 3` and the same for
   `VRSKY_DLQ`; wait until all peers are current.
2. `nats kv del` the three unused buckets (frees the 61 GiB reservation).
3. Apply the ConfigMap with `max_file: 6G`.
4. `kubectl delete statefulset nats-platform --cascade=orphan`; apply the
   StatefulSet with an 8 GiB volume template.
5. For each of `nats-platform-2`, `-1`, `-0`: delete the pod and its PVC;
   the StatefulSet recreates both at 8 GiB; wait until the server rejoins
   and every stream reports all peers current.
6. Set both streams back to R1.

The ops run from a short-lived `nats-box` pod in `vrsky-platform`
(through `in-cluster.sh` or by hand). Each old disk is snapshotted before
its PVC is deleted; the snapshots are deleted after a week.

### 3. MinIO

New PVC `minio-data-small` (8 GiB); scale MinIO to 0; a one-off pod
mounting both copies the data (`cp -a`); point the Deployment at the new
claim; scale to 1; check a spill round-trip (the claim-check test path);
snapshot and delete the old PVC.

### 4. Repo

| File | Change |
|---|---|
| `infrastructure/kubernetes/platform-nats/statefulset.yaml` | volume template 100Gi → 8Gi |
| `infrastructure/kubernetes/platform-nats/configmap.yaml` | `max_file: 90G` → `6G` |
| `infrastructure/kubernetes/platform-nats/kv-setup-job.yaml`, `deploy-vrsky-platform.sh`, `platform-nats/README.md` | job removed |
| `infrastructure/kubernetes/minio/deployment.yaml` | claim `minio-data-small`, 8Gi |
| `infrastructure/azure/deploy-azure.sh` (pool comment) | the pool name and OS disk type |
| `docs/operator/cost-parking.md` | the floor and what it is made of |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestPersistentVolumesStayWithinBudget` |

### 5. A guard

`TestPersistentVolumesStayWithinBudget`, beside the CPU budget guard:
sums the storage requests of every persistent volume the production
manifests declare (StatefulSet templates × replicas, PVCs, the monitoring
values) and fails above the budget, naming the largest; and fails if
NATS `max_file` is not below the per-replica volume size. The live
volumes add up to about 477 GiB today (NATS 300, MinIO 100, Postgres 50,
Prometheus 20, Grafana 5, Alertmanager 2); after the change about 101,
so the budget is set when the guard is written, just above what remains.

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestPersistentVolumesStayWithinBudget` | the declared volumes fit the budget; `max_file` < NATS volume | NATS back to 100Gi; `max_file` above the volume |
| Existing `TestCPURequestsStayWithinBudget`, `TestNodeConfigRulesAreDeployed` | nothing else moved | — |

## Verify in prod, per step

1. OS disk: `az aks nodepool list` shows one pool, `osDiskType:
   Ephemeral`; all pods Running; smoke ok; a stop and a start bring it
   back.
2. NATS: three servers on 8 GiB volumes, `VRSKY_DATA` message count the
   same before and after, all durable consumers present, the three
   in-use consumers restored their pipelines, smoke ok.
3. MinIO: 8 GiB claim bound, a large-payload message (> 256 KiB) goes
   through a pipeline end to end.
4. A week later: Cost Management shows the disk meters down.

## Risks

- **NATS quorum during the swap**: replacing one of three keeps two
  servers; the R3 streams keep quorum. Do it outside 16:00–07:00 so Park
  does not stop the cluster mid-swap (it queues behind nothing, it is not
  a deploy).
- **Ephemeral OS** loses the node's local state on reimage; nothing of
  VRSky lives on the node disk (all state is in persistent volumes).
- **8 GiB fills up** only if `VRSKY_DATA` grew past its 512 MiB cap,
  which it cannot (`DiscardOld`); the guard keeps `max_file` honest.

## Non-goals

Postgres and Prometheus volumes; LRS instead of ZRS (pennies at these
sizes); Azure Files `vrsky-files` (billed by use, not provisioned size).
