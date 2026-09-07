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

## A pipeline deploys but no data flows

1. Confirm it's **running** (Settings/Connections, or the connection status).
2. For a **webhook** source, POST to `http://<ingress>/webhook/{connectionId}`
   (local: `localhost:9100`) and watch the producer's live event panel.
3. Check the **DLQ** for the connection — failed messages land there with the
   error; retry or discard from the UI.
4. Tail the worker logs in Loki filtered by `connection_id`.

> `file-producer` has no live UI panel — tail `docker compose logs -f file-producer`.

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

**Fixes**, in order of preference:

1. Point the machine's DNS at a public resolver (1.1.1.1 or 8.8.8.8), or turn
   off the ISP filter on the subscription.
2. Pin the host locally. Fastest, but it hardcodes the IP — if the ingress
   LoadBalancer address ever changes you get a fresh, confusing failure until
   you update the line:
   ```bash
   echo "20.251.107.2 20.251.107.2.sslip.io" | sudo tee -a /etc/hosts
   ```
3. Use the real DNS name once it exists. This removes the whole class of
   problem and is the only fix that helps **partners** — a sender behind an
   affected resolver cannot reach a `sslip.io` webhook URL at all, and sees
   only a TLS error with nothing pointing at DNS.

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
