# #814: NATS consumer for an external JetStream

## Goal

A source connector, `nats-consumer`, that reads from a NATS JetStream server
VRSky does not run: a durable pull consumer on a stream (with an optional
subject filter), explicit ack. It is how VRSky takes Retailhero's
`rh.evt.v1.<tenant>.…` events from a per-tenant partner account
(ValueRetail/Retailhero#2675, decision of 2026-11-10).

## Acceptance criteria

1. Config `config.nats`: `servers` (one or more URLs), `auth_type`
   (`none`, `creds`, `user_password`), `username`, `password_secret_id`,
   `creds_secret_id` (a JWT/NKey `.creds` file), `ca_cert` (PEM, optional),
   `stream`, `durable`, `filter_subject` (optional), `batch_size` (optional,
   default 10, max 256).
2. Credentials come only from stored secrets. A stored node config that
   carries a plaintext `password` or `creds` is refused at start with
   status `error`. Credentials never reach a log line or `last_error`.
3. Each message is published into the pipeline with an envelope id derived
   from the connection id and the source `Nats-Msg-Id` (fallback: stream
   name and stream sequence). That id is the VRSky `Nats-Msg-Id`, so
   JetStream drops a repeat inside VRSKY_DATA's five-minute window.
4. The source message is acked (double ack) only after VRSky's publish was
   acknowledged. A failed publish is retried in place with a growing wait,
   the held messages kept in progress (no redelivery, no delivery spent), so
   order holds. Pull requests are closed before messages are processed (a
   no-wait pull, then a one-message wait when there was nothing), since the
   server redelivers expired messages into an open request.
5. Connection loss and other transient errors reconnect with a capped
   backoff. Bad credentials, a refused address, TLS failures, a missing
   stream, JetStream not enabled, a permission violation, or a durable that
   does not fit (push consumer, not explicit ack, other filter), a durable
   deleted after the loop bound it, or a message too large to publish stop
   the connection with status `error` and a reason in `last_error`.
6. Server URLs go through `connectorurl.NATS` at start, in the management
   API's start check and in the test endpoint: scheme `nats` or `tls`, a
   host, no userinfo, path, query or fragment, no literal private address.
   The dial goes through `safeurl.NewDialer`, which refuses private,
   loopback and link-local addresses after name resolution. Without
   `ALLOW_PRIVATE_UPSTREAMS` TLS is required. Discovered cluster servers are
   ignored so the client only dials what the tenant configured.
7. Tests on embedded JetStream: a second embedded server plays the external
   one. Covered: publish then ack, the envelope id from `Nats-Msg-Id` and
   the fallback, redelivery after a crash before ack with no duplicate in
   VRSKY_DATA, failed publish leaves the source unacked, user/password and
   JWT creds auth, bad credentials and unknown stream end in `error`,
   reconnect after the external server restarts, plaintext credentials
   refused, the URL and dial checks.
8. Registered everywhere `rabbitmq-consumer` is; docs page
   `docs/connectors/nats.md`; UI source type with a config editor and the
   test button.

## Packages and files

- New: `src/cmd/nats-consumer/` (main, service, config, source, server,
  Dockerfile, tests).
- `src/pkg/safeurl`: exported `NewDialer` (the dial-time check for non-HTTP
  clients).
- `src/pkg/connectorurl`: `NATS(servers, allowPrivate)`.
- `src/pkg/managementapi`: `nodeconfig.go` rule, `connector_urls.go` check,
  `connection_test_endpoint.go` route, tests.
- Registration: `infrastructure/azure/{deploy-connectors-azure.sh,
  build-push-acr.sh, affected.sh, postgres-tls.sh}` (the last one's
  EXPECTED_CONNECTORS goes 31 → 32, pinned by a guard test), regenerated
  `infrastructure/kubernetes/connectors/connectors.yaml`,
  `infrastructure/prometheus.yml`, `docker-compose.yml`, `.gitignore`,
  `.github/workflows/integration.yml` (only if the connector has an
  integration-tagged test; it does not, so untouched),
  `docs/DEPLOYMENT_GUIDE.md`, `docs/connectors/index.md`, `mkdocs.yml`,
  `docs/llms.txt`.
- UI: `ui/src/components/Pipeline/NatsConfigEditor.tsx` (+ test),
  `PropertyEditor.tsx` (source type), `ui/src/utils/secrets.ts` (`creds` is
  a secret field).

## Decisions made without asking

- Envelope id is a UUIDv5 of connection id + source key, the same shape as
  the webhook consumer's Idempotency-Key ids. Two connections reading the same
  event get different ids, so one does not swallow the other's publish in the
  shared VRSKY_DATA dedup window.
- No durable (Postgres) dedup store. JetStream's five-minute window covers a
  redelivery after AckWait (30 s when we create the durable). A crash in the
  milliseconds between VRSky's publish ack and the source ack, followed by
  more than five minutes of downtime, can still duplicate one message.
  Backlog item added.
- The durable is created when missing (pull, explicit ack, deliver all,
  AckWait 30 s, unlimited MaxDeliver so a long VRSky outage loses nothing).
  An existing durable is used as it is and never updated; one that does not
  fit is a permanent error, so VRSky never rewrites a consumer someone else
  owns.
- `nats.NoReconnect()` plus our own redial loop, so every reconnect goes
  through the same error classification (an auth failure after a credential
  rotation becomes `error`, not an endless retry).
- No sample-data endpoint in this PR. The SDK's last_payload preview covers
  the builder once data has flowed. Backlog item added.
- The test endpoint accepts a draft with plaintext credentials, the same as
  every other connector's: nothing is stored, and the stored config is what
  rule 2 guards.
- Port 9340 for the worker HTTP surface (next free in the 92xx–93xx block).
- Single replica (consumers drive their own ingestion; matches the others).
- `integration.yml` untouched: the new tests run on embedded NATS in the
  normal `go test` job and need no `integration` build tag.
- The management API reaches the NATS worker's test endpoint through
  `workerAddrTemplate()`, not a bare compose name like the other workers,
  so the Test button works in AKS. The others are a backlog item (201).
- The worker answers a connection test within 8 s, inside the management
  API's 10 s budget.
- The ack after a successful publish is not bound to the loop's context: a
  publish that lands just before a stop still gets acked.
- `.creds` is edited in a text area, since a password box flattens its
  lines; `creds` joins the UI's SECRET_FIELDS so deploy mints a secret.
- Envelope id input is `len(connection id) ":" connection id key`, so no
  key can collide with another connection's.

## Review round 1 (fresh Opus reviewer)

Fixed: postgres-tls tests now count the fleet from connectors.yaml; publish
retried in place instead of nak and refetch (order, MaxDeliver); a comma in a
server URL refused and the list passed as a list; tenant-visible texts through
`safeurl.UserMessage` (#838), no x509 detail in last_error; the servers box
keeps what is typed; a deleted durable is not silently recreated; pings every
20 s; a permission violation counts once; only the two credential secret ids
are resolved; .creds markers checked; the start test polls instead of
sleeping; a warning for a durable with MaxDeliver. Left as backlog: the Kafka
brokers box has the same typing bug; the workers' test endpoints have no
authentication of their own.

## Risks

- A new connector type means a new Dockerfile, image and Deployment: the
  merge is a production deploy of a new pod. `agent:needs-human`.
- The connector dials out of the cluster on 4222 (or the partner's port).
  AKS has no egress NetworkPolicy on vrsky-platform today, so nothing to
  change, but an operator adding one must allow it.
- Retailhero's partner account (Retailhero#2680) does not exist yet. The
  permissions it grants decide whether VRSky can create its durable; if it
  cannot, Retailhero must pre-create it and VRSky binds to it.

## Verification

`go build ./...`, `go vet ./...`, `golangci-lint run`, `go test -race` on
`cmd/nats-consumer`, `pkg/safeurl`, `pkg/connectorurl`,
`pkg/managementapi`, `make lint-tenant`, `make lint-connector`; UI `tsc -b`,
lint, build, vitest with coverage; a mutation run on the ack-after-publish
order and the dedup id.
