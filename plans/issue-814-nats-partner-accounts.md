# #814 follow-up: NATS source on a Retailhero partner account

## Why

#844 shipped the `nats` source and closed #814. It cannot read from the
account Retailhero actually hands out (Retailhero#2680,
`docs/operations/local-receiving.md`, "VRSky partner accounts"):

- the user is a bare NKey (a seed, no JWT), and #844 knows only `.creds`
  and user/password;
- the account has no JetStream of its own and imports the API under
  `JS.RH.API`, so requests to `$JS.API.>` get "JetStream not enabled";
- the user may subscribe only to `_INBOX_vrsky_t_<x>.>`, so replies to the
  default `_INBOX` prefix never arrive;
- the account has no `STREAM.INFO` grant, and #844 binds through
  `js.Stream()` (stream info) before it looks up the durable;
- the create grant carries the durable and filter in the subject, so VRSky
  may create its durable only with the exact filter; a tenant may want to
  forbid creation altogether.

## Acceptance criteria

1. `auth_type: nkey` with `nkey_seed_secret_id`. The seed must be a user
   seed (`SU…`), raw or decorated. A plaintext `nkey_seed` in a stored
   config is refused at start, like `password` and `creds`. The seed never
   reaches a log line, `last_error` or the test endpoint's answer.
2. `api_prefix` (optional, e.g. `JS.RH.API`): every JetStream API request
   goes to `<prefix>.…` instead of `$JS.API.…`. Acks keep their `$JS.ACK`
   reply subjects.
3. `inbox_prefix` (optional, e.g. `_INBOX_vrsky_t_a`): every reply inbox,
   for API requests and pulls, uses it.
4. The durable is bound by name with `CONSUMER.INFO` only; no
   `STREAM.INFO` request is made. A missing stream is still reported as
   "stream does not exist" (from the consumer info error code).
5. `create_durable` (optional, default true as before): false binds only;
   a missing durable is then a permanent error at every start.
6. `max_wait_seconds` (optional, 1–30, default 5): how long a waiting pull
   lasts. `batch_size` stays as it is.
7. `ca_cert` may also come from a stored secret (`ca_cert_secret_id`).
8. Errors: without `api_prefix` on such an account, without
   `inbox_prefix`, with a wrong seed, or with an unknown durable when
   creation is off, the pipeline ends in `error` with a reason that points
   at the setting. Connection loss stays retriable.
9. The management API start check refuses an unknown `auth_type`, a
   credential auth type without its `*_secret_id`, a plaintext credential,
   and a prefix with wildcards or spaces.
10. Tests on embedded NATS with a server config shaped like Retailhero's
    rendered one (application account with JetStream, a partner account
    with one NKey user, exact service imports under `JS.RH.API`, inbox
    `_INBOX_vrsky_t_a`, TLS with a test CA): read and ack, redelivery
    after a crash before ack with one copy in VRSKY_DATA, idempotent
    replay of the same `Nats-Msg-Id`, bad seed permanent, missing prefixes
    permanent, unknown durable permanent, VRSky creating its own durable
    through the filtered create grant.
11. UI: the NKey option, prefixes, max wait and the create switch in
    `NatsConfigEditor`; `nkey_seed` minted as a secret on deploy.
12. Docs: `docs/connectors/nats.md` gets a Retailhero partner account
    section and the new settings.

## Files

- `src/cmd/nats-consumer/{config,source,service,server}.go` and tests,
  new `partner_test.go`.
- `src/pkg/managementapi/nodeconfig.go` (+ test).
- `ui/src/components/Pipeline/NatsConfigEditor.tsx` (+ test),
  `ui/src/utils/secrets.ts`.
- `docs/connectors/nats.md`.

No Dockerfile, manifest or workflow changes: #844 registered the worker.

## Decisions

- `create_durable` defaults to true so configs saved since #844 keep
  working. Retailhero's operator creates the durables with `messaging-up`;
  the docs recommend `create_durable: false` there.
- The stream's MaxMsgSize no longer feeds the pull budget (it came from
  stream info). The budget uses the server's `max_payload`, which bounds
  every message anyway.
- `ca_cert` stays a plain config value in the builder (a CA certificate is
  public); `ca_cert_secret_id` is accepted for API users who keep it in the
  vault.
- Idempotency stays on `Nats-Msg-Id`. Retailhero's relay sets it to the
  event id (`relay.rs`), so `event_id` and `Nats-Msg-Id` are the same key.
  A durable dedup store beyond the five-minute window is still #845.

## Review round 1 (fresh Opus reviewer)

Changes requested, nothing blocking. Fixed in 3f7945f7:

- A pull the user may not send (no `MSG.NEXT` grant) got no answer and
  looked like an empty stream, with the pipeline `running` forever. The
  permission violation now ends it with status `error`. The bug dates from
  #844; exact per-durable grants make it likely here. Test: the partner
  server without the pull grant.
- The start check now refuses `batch_size` above 256 and
  `max_wait_seconds` above 30 or fractional, like the connector. The UI
  stores whole numbers.
- The `api_prefix` and `inbox_prefix` hints appear only when the setting
  is unset, and say "if".
- The read test asserts the double ack's confirmation comes back across
  the account boundary.
- Nits: the NKey comment says what is wiped; the `api_prefix` label only
  when one is set; the 255-character limit in the start check; the deliver
  policy hidden when creation is off; the test note and docs say the
  create permission is not checked.

## Manual check

`nats:2.12` in Docker with the same rendered config (TLS, partner
account, grants), through `go test -overlay` so no file was added: two
events stored and acked, no ack failure, no seed in the log, and without
`api_prefix` the run ends in `error` with the hint. The container was
removed.

## Risks

- Secrets handling changes (a new credential type): `agent:needs-human`.
- Dropping stream info changes the bind path for every NATS source. The
  #844 tests run unchanged against it.

## Verification

`go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test -race -count=1 ./...`, `make lint-tenant`, `make lint-connector`;
UI `npx tsc -b`, lint, build, `npx vitest run --coverage`. A manual run
against `nats:2.12` in Docker with the same two-account config.
