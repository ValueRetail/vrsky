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

MinIO's own images are gone. Docker Hub deleted `minio/minio`, `minio/mc` and
`minio/kes` on 2026-09-11 (`pull access denied … repository does not exist`),
and on 2026-09-24 `quay.io/minio/minio` and `quay.io/minio/mc` started
returning `401 UNAUTHORIZED` for every tag. A node that still has an old image
cached keeps running; one that has to re-pull cannot. On AKS that is every
node after `az aks start`: the pool uses ephemeral OS disks, so nothing stays
cached. Check with:

```bash
kubectl -n vrsky-storage describe pod -l app=minio | grep -A3 "Failed to pull"
```

The manifests, `docker-compose.yml` and the DR drill now use the pinned
community fork `pgsty/minio` and `pgsty/mc`. Prod runs a copy of those tags in
ACR, so an upstream deletion cannot stop it starting:

```bash
infrastructure/azure/minio-images-acr.sh             # import into ACR (works while the cluster is stopped)
infrastructure/azure/minio-images-acr.sh --repoint   # once the cluster is up: point MinIO at the ACR copy
```

`deploy-azure.sh` does both on a fresh bring-up. To move to a newer release,
bump the `pgsty/*` tags in `infrastructure/kubernetes/minio/` and run the
script again.

KES (`quay.io/minio/kes`, `infrastructure/kubernetes/encryption/`) still pulls
from quay.io; if it starts failing the same way, it needs the same treatment.

## No alerts arrive in Teams (or Slack), although something is clearly wrong

Follow the chain from the end:

1. **Does the target work at all?** Settings → Notifications → **Test** on the
   target. A failure here is the webhook URL (regenerate it in Teams) or, for
   email, missing `SMTP_*` on the management-api.
2. **Is the alert firing?** Port-forward Prometheus
   (`kubectl -n vrsky-monitoring port-forward svc/prometheus-prometheus 19090:9090`)
   → `/alerts`. Not there: the metric is missing — check `/targets` for the
   management-api and connector PodMonitors being **up**, and
   `vrsky_connections` / `vrsky_remote_agent_online` returning series.
3. **Did Alertmanager deliver?** `kubectl -n vrsky-monitoring logs
   alertmanager-prometheus-alertmanager-0`. A `401` from the webhook means the
   `alerts-webhook-token` Secret in `vrsky-monitoring` and the
   `ALERTS_WEBHOOK_TOKEN` env on the management-api differ. A `503
   NotConfigured` means the env was never set — `deploy-core-azure.sh` does
   not apply env; patch the deployment (monitoring README).
4. **Did the management-api route it to a target?** Its log line
   `alerts webhook` shows `delivered`/`failed`. `delivered: 0` with no error
   means no target matched: a tenant alert needs a target **in that
   workspace**; a platform alert needs a target with **platform** ticked;
   `min_severity` on the target may filter it.

## Login answers "too many failed sign-in attempts"

That is the failed-attempt limit (plans/login-rate-limit.md), and the API
answered `429` without checking the password. Two things trip it:

- **Ten failed logins from one network address** — any accounts — then one
  more attempt per minute. An office shares one address, so ten mistypes
  between colleagues with no success in between is enough. Successful logins
  never count.
- **Ten failed logins against one account** from anywhere, then one per five
  minutes. This is what stops password guessing, and it also means someone
  who knows an email can keep its owner on the password form — *except* from
  an address that has logged in to that account in the last 30 days, which
  is still let through.

The message is the same in both cases, on purpose: it must not say whether
the account exists. Sign-up (`POST /api/v1/auth/register`) has its own
limit, five requests per address, then one per ten minutes.

Open sessions and SSO sign-in are not affected. To see what is going on:

```bash
kubectl logs -n vrsky-platform deploy/vrsky-management-api --since=1h | grep "Sign-in attempts blocked"
```

Every management-api line is JSON (`service`, `level`, `msg`, then the
fields), these included.

## Password-reset mail is not sent

`kubectl logs -n vrsky-platform -l app=vrsky-management-api --all-containers | grep "reset mail"`
names the reason. `535 5.7.3 Authentication unsuccessful` from
`smtp.office365.com`, with `530035 Access has been blocked by security
defaults` in the Entra sign-in log, means the tenant blocks SMTP
authentication: send through Microsoft Graph instead (docs/operator/mail.md).
`invalid_client` from the token endpoint means the Graph client secret has
expired or was mistyped.

One line per block, with the scope (`address` or `account`) and the key. The
same event is in `auth_audit_log` with status `blocked`, and
`vrsky_auth_limited_total{endpoint,scope}` counts refusals. To change the
allowance, or switch a limit off, without a build:

```bash
kubectl set env -n vrsky-platform deploy/vrsky-management-api AUTH_LOGIN_MAX_FAILURES=20 AUTH_SIGNUP_MAX_ATTEMPTS=5
```

`0` turns that limit off. The counters live in memory per replica, so with
two replicas the effective allowance is up to twice the number, and a deploy
forgets them.

## Pipelines will not start: `402 PlanRequired`

The workspace's trial has ended and no plan is active (`docs/operator/billing.md`).
Its pipelines were stopped by the billing sweep and the UI shows a red banner.
Reading and editing still work. The owner asks for a plan under Settings → Plan;
you activate it under Platform → Workspaces, which also starts the stopped
pipelines again. To extend a trial instead, set plan Trial with a new end date.

## "`<setting>` holds a {secret:…} placeholder"

A node refers to a stored secret, as `{secret:<uuid>}`, in a setting that is
not a credential, or inline in a credential that takes only a
`<field>_secret_id` reference (#853). The list of settings that may, per
connector, is in [SECURITY.md](../SECURITY.md#where-a-secret-may-be-referenced).
Save, start, Test Connection and schema discovery refuse it with this message.
A node stored before #853 fails the same way at runtime: a source goes to
status `error` (or logs it and stays idle), a destination retries the message
and dead-letters it with this text in `X-Vrsky-Last-Error`. Fix the setting
(the plain value, or the credential's `_secret_id` reference), restart the
pipeline and replay the DLQ. A `<field>_secret_id` on a setting that is not a
credential is not an error: it is ignored, and the setting reads as unset.

A missing secret now reads `resolve <setting>_secret_id: stored secret not
found`, without the secret's id or the workspace.

## A builder panel says "cannot reach the data-converter service"

The builder's live panels (Converter, Filter, File Watcher, …) are served by
the management API, which proxies each worker's event stream at
`vrsky-<worker>.vrsky-platform.svc.cluster.local:<port>`. That name needs a
Kubernetes Service. The transforms did not have one until 2026-09-28 — they
are core services deployed from their own directories, unlike the connectors,
whose Services `deploy-connectors-azure.sh` generates — so every Converter and
Filter panel in prod failed this way while the pipelines themselves ran fine.

```bash
kubectl -n vrsky-platform get svc vrsky-data-converter vrsky-data-filter
```

If either is missing, apply `infrastructure/kubernetes/<worker>/service.yaml`
(`deploy-azure.sh` and `deploy-vrsky-platform.sh` do). The Services are
headless on purpose: each replica keeps its own event hub, and the proxy opens
one stream per pod so the panel sees all the traffic, not half. A test,
`TestWorkerEventSourcesHaveKubernetesServices`, now fails if a proxied worker
has no Service manifest.

## After a connector restart, do I need to redeploy pipelines?

No, not since 2026-10-07. Every standing consumer brings the pipelines the
database lists as `running` back by itself when it starts (`Restored running
pipelines count=N` in its log, within seconds of the pod being Ready). A
webhook source is reachable again as soon as its consumer is up. If a
pipeline did not come back, its consumer logged why at start (a config that
no longer resolves, a missing secret); fix that and Stop/Start it. A
database that does not answer at that moment is retried rather than taken
as an answer, see the next section.

## A pipeline says `running` but its consumer did not start

Since #725 every command-driven consumer except the remote agent gateway
retries a start that failed for a reason that passes. Its log shows
`Start failed for a reason that may pass; retrying in the background`, then
`Retrying start attempt=N`. What counts as passing: the management database not answering (connection refused or reset,
a timeout, the server starting up or shutting down, too many connections)
while the start reads the connection or its secrets, and for the database
source, the source database not answering. The first retry comes after about
5 s, the wait doubles up to 5 min, and the consumer gives up 6 h after the
first failure (`Start still failing; giving up`). The status stays `running`
while it retries; nothing is written until the start works.

The retries end on their own when the connection is stopped, when its row no
longer says `running`, and when the consumer shuts down; a new pod restores
the connection again. A start command (Stop/Start, or Start alone) while a
retry waits runs at once. Retries live in the consumer process that saw the
failure: each pod has its own, and knows nothing of another pod's.

A start that fails for a reason a retry does not fix (bad config, a refused
URL or address, credentials refused, a missing secret, a refused
`{secret:…}` placeholder) is not retried. The consumer logs why and the
pipeline stays down: fix the cause and Stop/Start it. Some consumers log a
secret they cannot read only at Debug, as "Not a … consumer"; if nothing
shows at Error, raise the log level or check the node's secrets. Most
consumers do not write status `error` for a refused start yet (#822), so the
status can still say `running`.

At boot, `Could not list running pipelines to restore; trying again` means
the consumer could not read which pipelines to bring back because the
database did not answer. It retries on the same schedule and restores them
once the database answers. Any other list error is logged once
(`The query failed for a reason a retry does not fix`) and nothing is
restored. The line
`nothing restored. Restart the consumer once the database answers`
means it gave up (6 h) or shut down first: restart the pod.

Deploying from the builder keeps the connection id and the webhook URL.
Deleting a connection does not — the builder asks before creating a new one.

## A pipeline deploys but no data flows

1. Confirm it's **running** (Settings/Connections, or the connection status).
2. For a **webhook** source, POST to `http://<ingress>/webhook/{connectionId}`
   (local: `localhost:9100`) and watch the producer's live event panel.
3. Check the **DLQ** for the connection — failed messages land there with the
   error; retry or discard from the UI. Not every failure ends up there: see
   [Which failed messages reach the DLQ](#which-failed-messages-reach-the-dlq).
4. Tail the worker logs in Loki filtered by `connection_id`.

> `file-producer` has no live UI panel — tail `docker compose logs -f file-producer`.

## Which failed messages reach the DLQ

Only some failures do. A destination (a producer in the code) sorts every
error it hits into one of two kinds:

- **Retried.** The message is delivered again after 1 s, 5 s, 30 s and 2 min.
  After the fifth attempt, about 2.5 minutes after the first, it is copied to
  the connection's DLQ with the last error, the attempt count and the
  worker's name, and kept there for 7 days. Retry it from the DLQ once the
  cause is fixed. The worker logs `Handler error, will retry` with `attempt`
  and `retry_in`. Two exceptions:
  - A rate limit (`429` from Sitoo or Front Systems; `429` or `503` from
    Visma, Brightpearl, Business Central or SAP) waits at least as long as
    the destination asked. Sitoo and Front Systems read it from the answer
    (`X-Rate-Limit-Reset` or `Retry-After`) and use 5 s when it has none; the
    other four always use 5 s. That wait is kept between 1 s and 2 min, and
    the message waits the longer of it and the step above, so a 5 s wait
    becomes 5 s, 5 s, 30 s and 2 min.
  - Each worker pod holds at most 8 messages waiting for their next attempt,
    and at most 4 per connection (#794). Each waiting message keeps one of
    the 32 places the worker's durable consumer has for unsettled messages,
    and every connection of a type shares that consumer, so without the
    limit one dead target could hold up every other customer's messages. A failed message
    over the limit is retried at once: its five attempts take about a
    second, and it lands in the DLQ. The worker logs `Handler error,
    retrying at once: too many messages waiting out a retry delay` and
    `vrsky_message_retry_undelayed_total` goes up. When a target is down,
    expect most of that connection's messages in the DLQ within seconds and
    the first few after about 2.5 minutes.
  - A worker that panics is handled like one that returned an error: retried,
    then dead-lettered with `X-Vrsky-Last-Error: handler panicked: …`.
  - If the copy to the DLQ fails on the last attempt, see
    [A message that could not be dead-lettered](#a-message-that-could-not-be-dead-lettered).
- **Dropped.** The error is marked permanent, because trying the same message
  again cannot work: the target refused the message itself (a `4xx` such as
  `400` or `422`), or the payload is not JSON. The message is acked and
  nothing is kept. There is no replay for it, and `vrsky_dlq_messages_total`
  does not count these. What the worker logs depends on how the drop happens.
  An error marked `sdk.Permanent` (the Sitoo, Front Systems, Visma,
  Brightpearl, Business Central and SAP destinations, and a non-JSON payload
  in the database and Salesforce destinations) is logged by the SDK as
  `permanent error; dropping message`. The HTTP destination's `4xx` is a
  plain ack: the feed shows it and the log has `HTTP request returned error`.
  Look in the worker's log in Loki, filtered by `connection_id`, and in the
  connection's activity feed.

An error in the node's own config or credential is retried, not dropped,
because the same message goes through once the node is fixed (#734): a
`401`/`403`, a setting the destination needs that is missing, a template that
cannot be rendered. Its five attempts fail the same way and it lands in the
DLQ with an error that names what to fix. Fix the node, then retry the
messages from the DLQ. Wait 5 minutes after the fix first: every destination
keeps a connection's config that long, so an earlier retry can meet the old
node and land in the DLQ again (cloud storage drops its copy when the
connection is deployed again). With several nodes of
the same Kafka, RabbitMQ, SFTP or cloud-storage destination on one
connection, one misconfigured node fails the whole message for that
destination, so its healthy sibling gets it again on each attempt and on the
retry (the HTTP and Salesforce destinations skip the nodes that took it). The
Sitoo, Front Systems, Visma, Brightpearl, Business Central and SAP
destinations still drop a `401`/`403` and an incomplete node; they follow in a
later change.

Which kind an error gets differs per destination:

| Destination | Dropped, not in the DLQ | Retried, then in the DLQ |
|-------------|-------------------------|--------------------------|
| HTTP | Every `4xx` except `401`, `403` and `429` (any other answer below `500` too) | `5xx`, `429`, network error (a request still unanswered after 30 s included), OAuth token failure; `401`/`403` (with OAuth, after the one token refresh); a node with no `url`, a URL a request cannot be built from, or a `tls` cert or key that does not parse (nothing is sent to that node); a refused address (private or loopback address, redirect to another host). With several nodes, only the failing ones are tried again and the last error names them (`node <id>: …`); a node that took the message is skipped, unless the worker restarted in between ([HTTP page](../connectors/http.md#failed-deliveries)) |
| Database | Payload not JSON | A target database that cannot be opened or reached, a row the database refuses (none of the message's rows stay in that target), a lost connection, failure to check for or create the table; a node with no `host` or a table name that is not `table` or `schema.table`. A target that committed is skipped when the message comes back (see the [database page](../connectors/database.md#several-destination-nodes)) |
| File | Output path outside the workspace's area or any mounted directory | Failure to write the file; failure to read the connection from the database |
| Cloud storage | A key that cannot be built even without the `key_template` (the payload's own `uuid` field cleans away to nothing); a `key_template` that cannot be rendered falls back to a generated key instead | No `bucket`; failure to open the provider or write the object |
| SFTP | Nothing | No `host`, `username` or `remote_dir`; no password or key; `filename_template` that cannot be parsed or rendered (a field the record lacks included); failure to connect (after three tries), to create `remote_dir` or to write the file |
| Kafka | Nothing | No `brokers` or `topic`; failure to open the writer or to write |
| RabbitMQ | Nothing | No `url`, or neither `exchange` nor a queue; failure to connect or to publish |
| Salesforce | Payload not JSON | No `object`; `upsert` with no `external_id_field`; a record Salesforce refuses with a `4xx` (the error names it as `record[N]` with Salesforce's code; the other records still go in), a record with no external-id value; `401`/`403` after the token refresh, `408`, `429`, `5xx`, a redirect, network or token failure; the same answers while creating, filling or closing a Bulk API job; a record a Bulk job refuses or never processes (named by index with Salesforce's code), a Bulk job not done within 2 minutes per attempt, Bulk results that cannot be matched to the records, a Bulk record with nothing to send. Records that went in are not sent again on the retry (see the [Salesforce page](../connectors/salesforce.md#records-are-not-sent-twice)); a DLQ retry of a Bulk `insert` after a restart duplicates them ([Retrying a Bulk message from the DLQ](../connectors/salesforce.md#retrying-a-bulk-message-from-the-dlq)) |
| Sitoo | Incomplete node; payload not JSON; a webhook event notification; every `4xx` except `429`, `401`/`403` included; a request that cannot be built | `5xx`, `429`, network error; a refused `base_url` or address |
| Front Systems | Incomplete node; payload not JSON; every `4xx` except `429`, `401`/`403` included; a request that cannot be built | `5xx`, `429`, network error; a refused `base_url` or address |
| Visma | Incomplete node; payload not JSON; every `4xx` except `429`, `401`/`403` included; a request that cannot be built | `5xx`, `429`, network error, token failure; a refused URL |
| Brightpearl | Incomplete node; payload not JSON; every `4xx` except `429`, `401`/`403` included; a request that cannot be built | `5xx`, `429`, network error; a refused base URL |
| Business Central | Incomplete node; payload not JSON; every `4xx` except `429`, `401`/`403` included; with `dedupe_fields`, a payload that is not a JSON object or a `401`/`403` on the lookup; a request that cannot be built | `5xx`, `429`, network error, token failure; a refused URL or address; with `dedupe_fields`, any other failed lookup (a `4xx` included) |
| SAP S/4HANA | Incomplete node; payload not JSON; every `4xx` on the write except `429`, `401`/`403` included | `5xx`, `429`, network error, token failure; any non-`2xx` on the CSRF token fetch (a rejected login shows up here), or an answer to it with no `X-CSRF-Token`; a request that cannot be built; a refused address |

Filters and converters do not use these rules. A payload they cannot parse is
acked with an `error` event in the activity feed. Fetching an offloaded
payload, storing a large result or publishing the result is retried and then
dead-lettered, and so is a record that lacks a field a converter mapping marks
`required`. So is a failed read of the connection's config. The remote
agent destination is described on its [own page](../connectors/remote-agent.md).

Four things apply to every destination in the table:

- A message whose envelope cannot be read is dropped, and so is a message for
  another connection (the shared consumers see every connection's messages
  and ack the ones that are not theirs).
- A payload that sits in the object store is fetched before delivery. If that
  fails, or the payload is over the buffering limit and the destination cannot
  stream it, the message is retried and dead-lettered. The HTTP, file and
  cloud-storage destinations stream a large payload instead of buffering it,
  except with several targets on the node (HTTP with OAuth too), where they
  buffer it.
- Reading the node's config comes first. A connection that no longer exists,
  or has no node for this destination, is not its business and the message
  is acked. A read that fails (the database query errors, or a secret on the
  destination's own node cannot be fetched or decrypted) is retried and then
  dead-lettered like any other retried error. Each failed attempt logs
  `connection config unavailable; message not acked, will retry` at error
  level with the `connection_id`. A database outage longer than the retry
  schedule (about 2.5 minutes) leaves the messages that were in flight in the
  DLQ; retry them once the database is back. Every destination's worker
  gives up on the same message, and their copies collapse into one DLQ entry
  only within the DLQ's 2-minute duplicate window; a worker that falls
  further behind writes its own. Several entries with the same envelope id
  are one message: retry only one of them, since a retry goes to every
  destination again. The `X-Vrsky-Worker` header says which worker gave up.
  If it is not the connection's destination, check whether the real
  destination already delivered the message before you retry it. After a
  full outage the surviving entry may name a bystander that never had to
  deliver anything, so treat that check as a precaution, not proof.
- With several destinations on one connection, one failure retries the whole
  message and the destinations that already delivered get it again.

## A message that could not be dead-lettered

On a message's fifth and last attempt the worker copies it to the DLQ. If
that copy fails, it tries twice more (after 500 ms and 2 s). If all three
fail (#811):

- The worker logs `Could not copy the message to the DLQ; it is left unacked
  and stays only in VRSKY_DATA` at error level, with `tenant_id`,
  `connection_id`, `durable`, `stream_seq`, `last_error` (why the delivery
  failed) and `error` (why the DLQ copy failed).
- `vrsky_dlq_publish_failed_total{pipeline_id, worker}` goes up. No alert
  is wired to it yet.
- The message is not retried again: NATS never delivers a message to the
  same worker more than five times. Usually it is not in the DLQ view
  either (but see below).

The payload is still in the `VRSKY_DATA` stream at that sequence, for up to
72 hours (less if the stream hits its size cap). There is no button for it
yet. First check whether a copy got into the DLQ after all: when the tries
timed out rather than being refused, one of them may have been stored with
its answer lost. The connection's DLQ view would then show an entry from
that worker at the time of the log line; to be sure, the entry's headers in
`GET /api/v1/connections/{id}/dlq` include
`Nats-Msg-Id: VRSKY_DATA-<stream_seq>`. If it is there, there is nothing to
recover. Otherwise, by hand: find the log line in Loki, read the message
with the NATS CLI (`nats stream get VRSKY_DATA <stream_seq>`, from a
nats-box pod), and once the cause is fixed publish its payload to
`vrsky.dlq.<tenant_id>.pipeline.<connection_id>`. It then shows in the
connection's DLQ view and can be retried or discarded there like any other
entry.

A failing DLQ copy usually means JetStream itself is unwell (a restart, a
full disk): check the NATS pod and its logs first.

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
