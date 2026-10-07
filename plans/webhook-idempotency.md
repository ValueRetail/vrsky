# Webhook idempotency keys + "skip if exists" in the BC destination

Bifrost's request (2026-10-07): a till's outbox delivers at least once, so a
lost 202 means the same sale arrives twice and Business Central gets two draft
invoices. Two layers: dedupe on `Idempotency-Key` in the webhook consumer
(durable, days), and an optional existence check in the BC producer for
redeliveries inside VRSky.

## Open questions

1. **Order of check / publish / remember.** Recommended: **check → publish →
   remember**, exactly as Bifrost asked (a failed publish leaves nothing
   behind, so the retry is a real second attempt). The gap is two identical
   requests *at the same moment*: both pass the check and both publish. That
   case is covered by the second layer below — `Nats-Msg-Id` derived from the
   key, which JetStream dedupes for 5 minutes — so no reservation row is
   needed. Alternative: reserve first and delete on failure; stricter, more
   states, not needed.
2. **How long to remember keys.** Bifrost says at least 7 days. Recommended:
   **30 days** — one small row per sale, and a till that was boxed up for a
   fortnight still gets the right answer. Constant, not config.
3. **Shape of the BC setting.** Recommended: `dedupe_fields`, a list of
   payload field names that are also the BC property names
   (`["externalDocumentNumber", "customerNumber"]`), as in Bifrost's example.
   A field missing or empty in a message means "cannot tell" → POST as
   today, with a log line. Alternative: an explicit payload→BC field map;
   more to configure, nothing it buys here.

## Goal

The same sale sent twice — seconds, hours or days apart — produces one draft
invoice. The till is always told 202 for a repeat so it stops retrying. A
publish failure is reported as such and the retry gets through. Connections
whose requests carry no `Idempotency-Key` are untouched.

## What exists (checked 2026-10-07)

- `cmd/webhook-consumer/server.go` `handleWebhook`: resolves the connection,
  optional mTLS, optional HMAC (`http.signature`), builds an envelope with a
  **random `ID`**, publishes through the SDK closure, writes `last_payload`,
  answers `202 {"status":"accepted","envelope_id":…}`. Publish failure → 500.
  The SDK publishes with `Nats-Msg-Id = envelope.ID` (`lifecycle.go:531` →
  `messaging.Publisher.Publish`), so JetStream's 5-minute window already
  dedupes *identical envelope IDs* — which a retry never has today.
- The consumer has `DATABASE_URL` in prod (reads `connections`); the
  checkpoint store (`pkg/checkpoint`, table in the same database, migrations
  run by management-api) is the precedent for connector-owned durable state.
- `cmd/business-central-producer/service.go`: per-node config
  `business_central{entity, method, …}`; `write` POSTs/PATCHes
  `companies(<id>)/<entity>`; 2xx acks, 429/503 rate-limit, 401/403 and other
  4xx poison, 5xx retry. No GET anywhere in the producer; the consumer has the
  OData paging/`$filter` pattern.
- **BC API v2.0 `salesInvoices` is an aggregate**: `status` is
  `invoiceEntityAggregateStatus` with values Draft, In Review, Open, Paid,
  Canceled, Corrective — i.e. posted invoices (Open/Paid) stay in the same
  collection as drafts, with `externalDocumentNumber` and `customerNumber`
  on both. One `$filter` covers Bifrost's "also posted" concern; no second
  lookup. (A Canceled one matches too, which is right: that sale was handled.)
- Both services are standing SDK connectors: after a restart, pipelines
  using them must be redeployed from the builder.

## Approach

### 1. `pkg/idempotency` — durable keys

Migration `000027_webhook_idempotency`:

```sql
CREATE TABLE webhook_idempotency_keys (
    tenant_id       VARCHAR(255) NOT NULL,     -- connections.tenant_id is varchar
    connection_id   UUID         NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    idempotency_key VARCHAR(64)  NOT NULL,
    envelope_id     UUID         NOT NULL,     -- what the first delivery became
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (connection_id, idempotency_key)
);
CREATE INDEX idx_webhook_idempotency_created ON webhook_idempotency_keys(created_at);
```

```go
type Store interface {
    Seen(ctx, tenantID, connectionID, key string) (envelopeID string, seen bool, err error)
    Remember(ctx, tenantID, connectionID, key, envelopeID string) error   // INSERT … ON CONFLICT DO NOTHING
    Expire(ctx, olderThan time.Time) (int64, error)
}
```

Postgres and in-memory implementations (tests, local runs without a
database). Every query carries `tenant_id` as well as `connection_id`; the
table joins the tenant-lint list.

### 2. Webhook consumer

In `handleWebhook`, after the signature check (an unsigned probe must not be
able to read or plant keys):

```
key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
if len(key) > 64 → 400 "Idempotency-Key longer than 64 characters"
if key != "" {
    if id, seen := store.Seen(…); seen {
        Idempotency-Replayed: true; 202 {"status":"accepted","envelope_id":id,"replayed":true}
        return                                  // no publish, no last_payload
    }
    env.ID = uuid.NewSHA1(ns, connectionID+"\x00"+key)   // Nats-Msg-Id: the 5-min second layer
    env.Metadata["idempotency_key"] = key
}
publish → on error 500 as today, nothing remembered
if key != "" { store.Remember(…) }   // a failed Remember is logged, still 202: the publish happened
```

- A `Seen` lookup that *fails* (database blip) does not block the sale:
  log, carry on as a first delivery. The second layer still catches a
  repeat within 5 minutes; a duplicate beyond that during a database outage
  is accepted and documented.
- `Access-Control-Allow-Headers` gains `Idempotency-Key`.
- An hourly `Expire(now − 30 d)` in the consumer (both replicas may run it;
  a delete is idempotent). Counter `vrsky_webhook_replayed_total{connection_id}`
  on the existing metrics endpoint.
- Nothing changes for requests without the header: same envelope IDs, same
  path, same tests.

### 3. BC producer — `dedupe_fields`

Config `business_central.dedupe_fields: ["externalDocumentNumber","customerNumber"]`
(POST only; ignored for PATCH, which is an update by design). Before the
POST, in `write`:

- Read each field from the top level of the payload JSON. A missing or empty
  value → log `dedupe: field X absent, posting`, POST as today.
- `GET <entityURL>?$filter=externalDocumentNumber eq 'T02-000009' and customerNumber eq 'C001'&$top=1&$select=id`
  (single quotes in values doubled — OData escaping; the connector never
  builds the filter from anything but the configured field names and the
  message's own values).
- `value` non-empty → log `already exists in Business Central` with the BC
  `id`, **ack** (return nil). Empty → POST as today.
- GET 401/403 → poison as today; 429/503 → rate-limited; other non-2xx or a
  network error → **retriable, no POST** — when BC cannot be asked, the
  message waits rather than risking a duplicate.
- Counter `vrsky_bc_producer_skipped_existing_total{connection_id}`.

### 4. UI and docs

- `BusinessCentralConfigEditor`, destination only: "Skip if a record already
  exists — match on fields" (comma-separated), stored as `dedupe_fields`.
- `docs/connectors/http.md` (webhook source): `Idempotency-Key` paragraph —
  what to send, what comes back, the 30-day window, the two layers.
- `docs/connectors/business-central.md` (destination): `dedupe_fields`, the
  salesInvoices example, that posted invoices are covered.

## Files

| File | Change |
|---|---|
| `infrastructure/migrations/000027_webhook_idempotency.{up,down}.sql` | the table |
| `src/pkg/idempotency/{store.go,store_test.go}` | Store, Postgres + memory, real-Postgres test via `pkg/testdb` |
| `src/cmd/webhook-consumer/{service.go,server.go,metrics.go,consumer_test.go}` | store wiring, header handling, replay answer, expiry ticker, counter |
| `src/cmd/business-central-producer/{service.go,producer_test.go}` | `dedupe_fields`, the lookup, classification, counter |
| `src/cmd/lint-tenant-filter/main.go` | `webhook_idempotency_keys` |
| `ui/src/components/Pipeline/PropertyEditor.tsx` | the field |
| `docs/connectors/http.md`, `docs/connectors/business-central.md` | as above |

Not changed: management API, the SDK, the publish path, NATS retention,
other connectors.

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestWebhook_IdempotencyKey_RepeatIsAcceptedNotPublished` | same signed body + key twice → 202 both, one message on the stream, `Idempotency-Replayed: true` and the first envelope id on the second | drop the `Seen` check |
| `TestWebhook_IdempotencyKey_DifferentKeysPublishTwice` | two keys → two messages | key everything by connection only |
| `TestWebhook_IdempotencyKey_PublishFailureForgetsTheKey` | publish fails → 500, nothing remembered; retry with the same key → 202 and one message | remember before publishing |
| `TestWebhook_IdempotencyKey_IsPerConnection` | the same key on two connections → two messages | drop `connection_id` from the lookup |
| `TestWebhook_IdempotencyKey_CheckedAfterSignature` | a bad signature with a known key → 401, and the key is not consulted or planted | move the check above the HMAC |
| `TestWebhook_IdempotencyKey_TooLong400`, `…NoHeaderUnchanged` | 65 chars → 400; no header → today's behaviour, random envelope id | — |
| `TestWebhook_IdempotencyKey_SetsNatsMsgID` | the published message's `Nats-Msg-Id` is stable for the key (embedded JetStream: a second publish within the window is deduped) | random id kept |
| `TestIdempotencyStore_Postgres` (real Postgres) | round trip, conflict is silent, tenant + connection scoping, `Expire` removes only old rows | drop `tenant_id` from `Seen` → also `lint-tenant` |
| `TestBCProducer_DedupeFields_SkipsWhenExists` | GET with the right `$filter` (escaped quotes), a match → no POST, ack, counter | skip the GET |
| `TestBCProducer_DedupeFields_PostsWhenAbsent` | empty `value` → POST as today | — |
| `TestBCProducer_DedupeFields_LookupFailureRetriesWithoutPosting` | GET 500 → retriable, no POST; GET 401 → poison | POST anyway |
| `TestBCProducer_DedupeFields_MissingFieldPosts`, `…IgnoredForPatch` | — | — |
| UI | the field round-trips to `dedupe_fields` | — |

Then `gofmt`, `go vet`, `golangci-lint`, `lint-tenant`, `go test -race ./...`
with the database, UI `tsc` + coverage.

**Local end to end (dev stack):** a webhook → BC pipeline is not available
locally, but webhook → file is: post the same signed body twice with one key
→ one file; twice with two keys → two files; kill NATS, post, 500, restore,
post again → one file. The BC half is covered by the fake-server tests and
by the prod acceptance below.

## Acceptance (prod, Bifrost's list)

After deploy, with the till (or curl with the connection's secret):

1. Same signed body twice, same key → 202 + 202, one draft invoice.
2. Same key two hours later → 202, still one.
3. Two keys → two invoices.
4. First try with NATS unreachable → 5xx; retry → 202, one invoice. (Done
   on the dev stack, not prod.)
5. `dedupe_fields` on, a forced redelivery (stop/start the pipeline with a
   message in flight, or the builder's resend) → "already exists", one
   invoice.
6. A connection without the header: unchanged.

## Rollout

Migration via management-api (`build-push-acr.sh core` →
`deploy-core-azure.sh management-api ui`), then rebuild `webhook-consumer`
and `business-central-producer` and `kubectl rollout restart` just those two;
**redeploy the pipelines that use them** from the builder afterwards (they do
not resume on their own). Bifrost sets `dedupe_fields` on the BC node.

## Risks

- **Two identical requests inside the same second** both publish; JetStream
  drops the second by `Nats-Msg-Id` for 5 minutes. Beyond that window only
  the table counts, and by then the first row exists.
- **Database unavailable at the moment of a retry** → the key cannot be
  checked, the sale is accepted again; the BC-side check is the net.
- **A key reused for a different body** is treated as a replay (the table
  does not hash bodies). That is what idempotency keys mean; documented.
- **`dedupe_fields` is one extra BC call per message** on that destination
  — fine for a till's sales rate; not switched on by default.
- The sample-data path writes `last_payload` without a tenant filter and the
  consumer is not under the tenant lint — pre-existing, listed under found.

## Non-goals

Body hashing; per-tenant key limits; a management-API view of keys; making
the BC lookup generic beyond `$filter` equality on configured fields; adding
`cmd/webhook-consumer` to the tenant lint (its existing `last_payload` and
sample-data queries would need their own fixes first — found, not fixed).
