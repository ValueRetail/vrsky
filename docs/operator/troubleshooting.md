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

## mTLS webhook handshake fails locally

macOS system `curl` (LibreSSL) mishandles EC client keys (`bad decrypt`). Test
with an OpenSSL-based curl (e.g. a Linux container). See the
[security whitepaper](../security/whitepaper.md).

## Consumer crash-loops on restart (older builds)

Historically an ack-wait/back-off mismatch caused re-subscribe failures; fixed.
If it recurs on a worker not rebuilt with the fix, remove the durable consumer
and restart: `nats consumer rm VRSKY_DATA <name> --force`.
