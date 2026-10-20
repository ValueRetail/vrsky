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

## Verifying after a start

```bash
kubectl get pods -n vrsky-platform
curl -fsS https://20.251.107.2.sslip.io/healthz
```

If the health check fails but pods are Ready, the problem is more likely DNS
interception of the `sslip.io` host than the cluster — see
[Troubleshooting](troubleshooting.md#https-fails-with-a-certificate-error-or-a-webhook-never-arrives).
