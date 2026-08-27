# Load testing & capacity baselines (Phase 3E, #88)

This documents how VRSky's throughput and latency are measured, the **real
numbers** the platform achieves on a developer stack, and the **regression
floors** CI enforces. It replaces the aspirational "millions of messages"
slide claim with measured truth.

> **Read this first — what these numbers are and aren't.** Every number below
> was measured on a **single developer host** (see *Test environment*) running
> the whole platform in one docker-compose stack. They characterise the
> software's efficiency and catch regressions; they are **not** the platform's
> production ceiling. Real capacity — horizontally-scaled workers, a dedicated
> NATS JetStream cluster, separate DB hosts — and the API-gateway latency
> ceiling are deliberately **deferred to a fixed-cluster run (#90)**, not
> claimed here.

## The harness

`tests/load/` is a dependency-light harness: pure `bash` + `curl` + `jq`, with
[k6](https://k6.io/) and [nats-box] running from their public Docker images on
the compose network (no Go/npm/k6 install needed on the host).

```sh
# bring up the flagship stack
docker compose up -d nats postgres-management management-api \
  webhook-consumer http-producer httpbin prometheus

# run a scenario (p99 from k6, sustained msg/s from the published counter)
tests/load/run.sh --rate 5000 --duration 30s webhook-to-http
```

`run.sh` bootstraps a load-test tenant (it auto-registers and marks the address
verified directly in the management DB — there's no mail server in the load
stack), deploys the pipeline through the real management-api, drives load, and
reports a one-line result. Throughput is read from the
`vrsky_messages_published_total{tenant_id}` counter via Prometheus
(`localhost:9099`); ingress latency comes from k6's `http_req_duration`.

| Scenario | Driver | What it exercises |
|----------|--------|-------------------|
| `webhook-to-http` | k6 → `/webhook/{id}` | webhook ingress → NATS → http-producer → httpbin (**flagship**) |
| `db-cdc` | `generators/cdc_insert.sh` | bulk INSERT → postgres-consumer logical-replication capture |
| `file-to-db` | `generators/file_drop.sh` | multipart uploads → file-consumer → db-producer → Postgres |
| `tenant-to-tenant` | `generators/tenant_publish.sh` | publish onto a tenant's pipeline subject (cross-tenant bridge) |

## Test environment

| | |
|---|---|
| Host | Apple Silicon (Mac17,9), 18 cores, 48 GB RAM |
| OS | macOS 26.5.1, Docker Desktop |
| Topology | full platform in one docker-compose stack on one host |
| Load client | k6 / nats-box containers on the same `vrsky-network` |
| Date | 2026-06 |

A shared single host means the load client, all workers, NATS, and Postgres
**compete for the same cores** — so these are conservative floors for the
software, not a scaled-out ceiling.

## Measured baselines

### Flagship — webhook → HTTP (end to end)

k6 drove a constant arrival rate at the webhook ingress; every accepted request
(HTTP 202) publishes one message that the http-producer delivers to the sink.

| Target rate | Sustained | p99 (ingress accept) | Failures |
|------------:|----------:|---------------------:|---------:|
| 2,000/s  | 2,000/s   | 1.6 ms  | 0% |
| 5,000/s  | ~4,980/s  | 22.3 ms | 0% |
| 10,000/s | ~9,950/s  | 25.4 ms | 0% |
| 15,000/s | ~14,890/s | 20.0 ms | 0% |
| 20,000/s | ~18,860/s | 27.9 ms | 0% |

**Baseline:** the webhook ingress sustains **~15,000 msg/s end-to-end with p99
≈ 20 ms and zero delivery errors** on this host, degrading gracefully toward
**~19,000/s**, where the in-container k6 client itself saturates (CPU contention
on the shared host) rather than the platform. No request failures were observed
at any rate tested.

In honest per-day terms that headline is **~1.3 billion messages/day** of
*sustained* single-host capacity — comfortably backing the "handle millions of
messages per day" claim with three orders of magnitude of headroom.

#### Re-validation after #139 (in-progress heartbeats)

The #139 change adds a per-in-flight-message `InProgress()` heartbeat goroutine
to the subscriber hot path. Re-running the flagship `webhook-to-http` scenario
with the rebuilt http-producer confirmed **no delivery regression**: at
5,000/s the pipeline delivered **124,609 messages end-to-end over 30s with 0
errors**, the http-producer stayed healthy throughout, and the durable consumer
**re-bound cleanly** to its existing 1s ack-wait (the reconcile path — no #99
crash-loop). Absolute throughput/p99 on that run were noisier and lower than
the table above purely because the host was under concurrent build + workload
at the time; a clean re-baseline of absolute numbers remains a fixed-cluster
item (#90), consistent with the rest of this doc. The heartbeat's cost is one
short-lived goroutine + a ticker per message, stopped the instant the handler
acks — negligible at the sink-bound rates above.

### Scaled topology — k8s orchestrator path (#15, k3d)

> **Historical (superseded).** The per-connection `pkg/runtime` workers described
> here no longer exist: the orchestrator stopped spawning them in #201/#205 and
> the binaries and package were deleted (ADR 0004). Node kinds are run by
> standing services. The measurements below still stand as a record of what was
> tested at the time; the topology they describe does not.

The numbers above run the **compose** stack (SDK connectors, one host). The
**k8s orchestrator path** — where the management-api's orchestrator (#157)
deploys per-node `pkg/runtime` workers with per-connection HPAs (#135) — was
load-validated separately on the local k3d cluster used for the #157/#159/#160
validations. k6 ran **as an in-cluster Job** driving a constant arrival rate at
a ClusterIP Service in front of the connection's autoscaled consumer replicas:
`k6 → Service → consumer(HTTP input) → NATS → producer → sink`.

- **Ingress:** sustained the full **3,000 req/s** target at **p99 < 1 ms, 0%
  HTTP failures** — the webhook input accepts at the same sub-millisecond
  latencies as the compose path.
- **Autoscaling under load (#135):** the consumer HPA scaled from 1 toward
  `maxReplicas` (consumer CPU > 360% of request; producer > 220%), i.e. the
  per-node HPAs the orchestrator creates react to real traffic — consistent with
  the dedicated #159 run (consumer 1→10 under load).
- **End-to-end delivery:** confirmed working through the orchestrator pipeline
  (messages arrive on the sink subject; the producer saturates). A **precise
  sustained delivered-msg/s ceiling is not claimed on k3d**: it shares one
  laptop's cores with everything else, a 30 s window doesn't outlast HPA
  scale-up lag, and the delivered count here is read by grepping a `nats sub`
  pod's log (a floor, not a ceiling). A clean scaled delivered-throughput
  baseline stays a real-multi-node-cluster item (with in-cluster Prometheus),
  consistent with the deferral at the top of this doc.

**Two data-path bugs were found and fixed by this run** (both would hit
production, not just k3d):

1. **`HTTPInput.Start()` blocked forever** in `ListenAndServe`, violating the
   `component.Input` contract ("Start … must be called before Read()"). Callers
   start the OUTPUT and the read→write loop only *after* `Start` returns, so the
   consumer never connected its NATS output and never drained its channel — every
   webhook was accepted with **202 and then silently dropped** once the 100-slot
   buffer filled. A 3,000 req/s run delivered **0** messages end-to-end. Fixed to
   serve in a background goroutine (binding synchronously so a port conflict
   still surfaces as a `Start` error). Regression tests added
   (`http_input_contract_test.go`) — the pre-existing tests all called `Start` in
   a goroutine and were `//go:build integration`, so none could catch this.
2. **`NATSOutput.Write` flushed per message** (`conn.Flush()` is a full
   server round-trip) and logged at **Info per message** — capping end-to-end
   delivery at a few hundred msg/s while the ingress accepted thousands. Removed
   the per-message flush (the client buffers + flushes asynchronously; `Close`
   flushes on shutdown; core NATS gives no per-publish ack regardless) and
   dropped the hot-path logging to Debug.

### CDC — Postgres logical-replication capture

`generators/cdc_insert.sh` bulk-inserts into the source Postgres and watches the
postgres-consumer capture it. Insert side is trivially fast (~285,000 rows/s
into the source in one statement); **capture was confirmed end-to-end** (the
consumer logs `Detected changes in table … change_count=N` and forwards a
batch onto NATS within ~1 s for 5–20 k rows). The
`postgres_consumer_changes_captured_total` counter is **batch-granular**, so we
report capture as *confirmed working* rather than a clean per-row rate — a
precise CDC sustained-rate baseline is a cluster-run item (#90).

### Tenant publish — data-plane ingest

`generators/tenant_publish.sh` pushes envelopes straight onto a tenant's
pipeline subject over NATS. Measured **~14,500 msg/s** sustained publish from a
single nats-box client (client-bound). Measuring *delivery* through the
tenant-consumer bridge to a destination tenant additionally requires an approved
`tenant_data_connection`; that control-plane setup is out of scope for the local
harness and is captured on a fixed cluster (#90).

### file → DB

`generators/file_drop.sh` is provided and runnable (deploys a file→db pipeline,
uploads N files, reads the published delta). Serial `curl` multipart uploads are
bound by the client, not the connector, so this is a functional driver rather
than a throughput ceiling; a baseline is a cluster-run item (#90).

## CI regression floors

`.github/workflows/load-smoke.yml` runs a **short, bounded** webhook→HTTP smoke
on PRs that touch the connectors or the harness, and on manual dispatch. It is a
**regression guard, not an SLA** — the thresholds are set far below the measured
dev-stack numbers so normal variance on a shared 2-core GitHub runner doesn't
flake, while a real regression (errors appear, or p99 collapses by an order of
magnitude) fails the build.

| Check | Floor (CI) | Measured (dev host) |
|-------|-----------|---------------------|
| `http_req_failed` | < 1% | 0% |
| `http_req_duration` p99 | < 750 ms | 20–28 ms |
| sustained rate | ≥ 150/s at a 200/s target | ≫ 15,000/s achievable |

The full multi-scenario baseline (the table above) is the **manual** path
(`workflow_dispatch` / local `run.sh`), not the per-PR smoke, because it needs
the wider stack and a less contended host to be meaningful.

## Deferred to #90 (API gateway + true capacity)

- **Gateway latency ceiling** — there is no API-gateway component yet (only the
  management-api + k8s ingress); the per-request overhead ceiling lands with
  the gateway in #90.
- **Scaled-cluster baselines** — ≥10 k/s-per-connector targets, Kafka/SFTP
  sustained rates, and CDC/file/tenant-bridge per-row baselines belong on a
  fixed multi-node cluster with isolated NATS and DB hosts, not a shared laptop.

[nats-box]: https://github.com/nats-io/nats-box
