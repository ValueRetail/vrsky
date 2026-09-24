# Troubleshooting

A field guide to the things that actually go wrong. Start with the logs
(`{service="…"}` in Loki, or `docker compose logs -f <service>`) and the
`/readyz` of the affected service.

## The terminal floods after `docker compose up`

You ran it **attached**. Use detached mode:

```bash
docker compose up -d --build        # -d = background
docker compose logs -f management-api   # follow one service on demand
```

## The production URL does not respond at all

Before treating this as an outage, check whether the cluster is simply parked:

```bash
az aks show -g vrsky-prod -n vrsky-prod --query powerState.code -o tsv
```

`Stopped` means someone deallocated the nodes to save money, which is the
normal state between pilots. Start it with `az aks start -g vrsky-prod -n
vrsky-prod` and give it several minutes. See
[Cost-parking the cluster](cost-parking.md).

A parked cluster is indistinguishable from an outage from the outside: the IP
still resolves (it is static), nothing answers on it, and no alert fires
because nothing crashed.

## MinIO or KES stuck in `ImagePullBackOff` after a cluster start

Docker Hub stopped serving the `minio/minio`, `minio/mc` and `minio/kes`
repositories in September 2026 (`pull access denied … repository does not
exist`). The manifests now pull the same tags from `quay.io/minio/*`. A node
that still has the old image cached keeps running; one that has to re-pull —
typically after `az aks start` lands a pod on a fresh node — cannot. Check
with:

```bash
kubectl -n vrsky-storage describe pod -l app=minio | grep -A3 "Failed to pull"
```

Apply the current manifests
(`infrastructure/kubernetes/minio/`, `infrastructure/kubernetes/encryption/`)
and the pod pulls from quay.io. If a compose or CI job hits the same error,
`docker-compose.yml` and the DR drill were moved in the same change.

## A pipeline deploys but no data flows

1. Confirm it's **running** (Settings/Connections, or the connection status).
2. For a **webhook** source, POST to `http://<ingress>/webhook/{connectionId}`
   (local: `localhost:9100`) and watch the producer's live event panel.
3. Check the **DLQ** for the connection — failed messages land there with the
   error; retry or discard from the UI.
4. Tail the worker logs in Loki filtered by `connection_id`.

> `file-producer` has no live UI panel — tail `docker compose logs -f file-producer`.

## The File Output panel says "Load failed", or shows no files

The panel and the upload box go through the management API
(`/api/v1/connections/{id}/files`). Before that they addressed the worker's own
port from the browser, using a URL baked into the bundle at build time — which
worked in compose and nowhere else, because those ports are not routable from a
browser and `VITE_*` values never reach a static nginx bundle. If you see this
on a deployment, check the UI image is current.

If the panel loads but lists nothing while the pipeline reports writes, compare
the two paths: output lands in `<mount>/<workspace-id>/<your path>`, so a
listing of the bare mount root is a different directory. The panel adds the
workspace segment for you; a `kubectl exec ... ls` does not.

```bash
kubectl -n vrsky-platform logs deploy/vrsky-file-producer | grep "file written"
kubectl -n vrsky-platform exec deploy/vrsky-file-producer -- ls -R /data/output/<workspace-id>
```

A `dropping: output path is outside the workspace's own files` line means the
configured directory is outside the mounted volume entirely — fix the path on
the node rather than widening the mount.

> Do **not** give `file-producer:9900` or `file-consumer:9200` an ingress to
> "fix" a panel that cannot reach a worker. Those endpoints have no tenant check
> of their own; publishing them exposes every workspace's files. See
> [Files](../connectors/file.md).

## Usage page shows 0 messages right after a test

The usage rollup reads Prometheus `increase()` and runs hourly; a quick burst
fired faster than the 15s scrape can be missed at the cold start of a counter.
Send sustained traffic, or wait for the next rollup. Not a data-loss issue —
the counter itself is correct.

## NATS restarted / OOM

The data stream is bounded (512 MiB / 1M msgs, discard-old) so a backlog can't
OOM the broker. If you see exit 137 on an older deployment, upgrade and confirm
`EnsureStreams` reconciled the cap (`nats stream info VRSKY_DATA`).

## HTTPS fails with a certificate error, or a webhook never arrives

Symptom: the platform URL gives a TLS error that makes no sense —

```
curl: (60) SSL: no alternative certificate subject name matches target host name
```

— or the page loads as a `302` to an ISP notice page, or a partner reports that
their webhook deliveries fail with a certificate error while everything looks
healthy in the cluster.

**Cause: an ISP DNS filter is answering for the host.** Some Norwegian ISPs
block wildcard-DNS services, and `sslip.io` is one of them. Telenor's *Nettvern*
filter resolves `<ip>.sslip.io` to its own block-page server instead of to the
real address. That server presents a certificate for its own name, which is
where the mismatch comes from — the platform's certificate is fine and was
never involved.

**Diagnose it in one command.** Compare your resolver against a public one:

```bash
echo "ISP:"; dig +short 20.251.107.2.sslip.io
echo "public:"; dig +short @1.1.1.1 20.251.107.2.sslip.io
```

Two different answers means interception. A blocked lookup looks like this:

```
ISP:
nettvern-info.telenor.net.
148.123.15.44
public:
20.251.107.2
```

**Confirm the platform is healthy** by bypassing DNS entirely. If this returns
the page, nothing is wrong server-side and you can stop investigating the
cluster:

```bash
curl -s --resolve 20.251.107.2.sslip.io:443:20.251.107.2 https://20.251.107.2.sslip.io/ | head -c 200
```

**The fix: use the real name.** `vrsky.valueretail.no` has existed since
2026-09-23 — an A record on Cloudflare, DNS-only, pointing at the same ingress.
It is not wildcard DNS, so no filter treats it specially, and it is the only
fix that helps **partners**: a sender behind an affected resolver cannot reach
a `sslip.io` webhook URL at all, and sees only a TLS error with nothing
pointing at DNS.

The `sslip.io` host still routes, so URLs handed out earlier keep working. If
you are looking at this page, the answer is almost always to stop using it.

Two stopgaps, if you are on an affected network and cannot change the URL:

1. Point the machine's DNS at a public resolver (1.1.1.1 or 8.8.8.8), or turn
   off the ISP filter on the subscription.
2. Pin the host locally. Fastest, but it hardcodes the IP — if the ingress
   LoadBalancer address ever changes you get a fresh, confusing failure until
   you update the line:
   ```bash
   echo "20.251.107.2 20.251.107.2.sslip.io" | sudo tee -a /etc/hosts
   ```

Two things worth knowing. First, the failure is asymmetric: it is invisible
from inside the cluster, where every check passes, so it is easy to spend a
long time on certificates and ingress before suspecting the network you are
sitting on. Second, this is not the first time — `docker-compose.yml` already
carries a `dns:` override on `webhook-consumer` for exactly this reason, because
Nettvern also blocks `api.trycloudflare.com` and breaks cloudflared quick
tunnels. If a hostname behaves impossibly, check the resolver early.

## mTLS webhook handshake fails locally

macOS system `curl` (LibreSSL) mishandles EC client keys (`bad decrypt`). Test
with an OpenSSL-based curl (e.g. a Linux container). See the
[security whitepaper](../security/whitepaper.md).

## Consumer crash-loops on restart (older builds)

Historically an ack-wait/back-off mismatch caused re-subscribe failures; fixed.
If it recurs on a worker not rebuilt with the fix, remove the durable consumer
and restart: `nats consumer rm VRSKY_DATA <name> --force`.
