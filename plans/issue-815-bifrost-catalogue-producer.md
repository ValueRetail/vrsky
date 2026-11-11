# Bifrost catalogue producer with staged import (#815)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A producer that delivers catalogue items to Bifrost's catalogue-api through its
staged import, so a full price list lands atomically and Bifrost's shrink guard
applies. The generic HTTP producer can do a delta one message at a time
(`PUT /v1/catalogue/items`) but cannot drive begin, items and commit.

## The contract (read from the Bifrost repo, not guessed)

Sources: `docs/catalogue-doors.md`, `crates/catalogue-api/src/routes.rs`,
`sessions.rs`, `crates/catalogue/src/format.rs` (`ImportItem`) and
`import.rs` (`CommitReport`, `CommitPolicy`).

| Call | Request | Answer |
|---|---|---|
| `POST /v1/catalogue/imports` | `{"kind": "full"\|"delta", "effective_from"?, "allow_shrink"?: "<reason>"}`; kind defaults to full; anything else is 400 | 201 `{"import_id", "kind", "effective_from"}` |
| `POST /v1/catalogue/imports/{id}/items` | `{"items": [ImportItem…]}`, optional `Idempotency-Key` (a repeated key inside one import is a replay: `received: 0, replayed: true`) | 200 `{"received", "total_so_far", "replayed"}` |
| `POST /v1/catalogue/imports/{id}/commit` | none | 200 `CommitReport` `{import_id, version, kind, applied, catalogue_size, rejected, errors[{row, article_no, error}], effective_from, live_now, shrink_override}` |
| `DELETE /v1/catalogue/imports/{id}` | none | 204 |

- Auth: `Authorization: Bearer <key>` on every call, permission
  `catalogue.import`. `X-Tenant-Id` is optional and must match the credential
  (one value, else 403). The tenant comes from the credential.
- Errors are `{"error": "<message>"}` (`ApiError::into_response`):
  401 bad or missing key, 403 wrong tenant header or permission, 400 unknown
  kind or blank shrink reason, 404 no such import (also another tenant's, on
  purpose), 422 the importer refused the contents (shrink guard: a full import
  under `min_fraction_of_previous` of the live catalogue; a file that is mostly
  bad rows), 429 too many imports open (cap 32), 500 body is the fixed text
  "internal error".
- Imports live in memory: a restart or the one-hour idle expiry loses them
  (404 afterwards). `commit` takes the import out of the table whatever its
  outcome (`sessions.take`), so a failed commit leaves nothing to abort and a
  later DELETE is 404.
- `ImportItem` (`format.rs`): `article_no`, `name`, `price_ex_vat` (string,
  commas refused), `vat_code`, optional `gtin`, `unit`, `category`, `min_age`,
  `alcohol`, `effective_from`. Bifrost#249 (open, PR #252 blocked on CI
  billing) adds `price_incl_vat` plus `vat_rate` as an alternative to
  `price_ex_vat`. The producer therefore forwards items verbatim and does not
  model the shape.
- Partial acceptance: `CommitPolicy.allow_partial` is true in practice. A
  commit can succeed with `rejected > 0` and per-row errors.

## Acceptance criteria

1. Node config `bifrost_catalogue`: `base_url`, `token_secret_id` (resolved to
   `token`), `batch_size`, `mode` full or delta; optional `tenant_id`,
   `allow_shrink_reason`.
2. One import per message. Any failure after begin abandons it (DELETE), so a
   retry never commits a half-filled import. A redelivery after a crash
   between commit and ack leaves the same catalogue.
3. Bifrost 4xx refusals (other than 401/403/404/429) are `sdk.Permanent` with
   Bifrost's reason in the text; 5xx, network errors, 404 and 3xx are
   retriable; 429 is `RateLimited`; 401/403 are retriable so the DLQ keeps the
   message for a replay after the node is fixed (#734).
4. Contract tests against an httptest stub of the four endpoints.
5. Docs page.

## Packages touched

- `src/cmd/bifrost-catalogue-producer/` (new): `main.go`, `service.go`
  (config, Deliver, import run, batching, node lookup), `client.go` (the four
  calls, error classification), `Dockerfile`, tests.
- `docs/connectors/bifrost-catalogue.md`, `docs/connectors/index.md`,
  `mkdocs.yml`.
- `.gitignore` (two lines; `TestGitignoreCoversCmdBinaries` fails without
  them).
- `plans/backlog.md`.

## Design

- Model: `sitoo-producer` (config lookup, secret resolution, route filter,
  `ServesConnection`, safeurl client). Transport errors use
  `safeurl.UserMessage` for the tenant-visible text and `safeurl.Detail` for
  the log, per #717/#838.
- **Batch window = one envelope.** The SDK acks on return from `Deliver`, so
  holding items across messages would ack messages whose items are only in
  memory. One message holds the list; `batch_size` (and a 4 MiB cap) only cuts
  it into requests. Payload: JSON array of objects or `{"items": [...]}`;
  `delta` also takes one object. Items are `json.RawMessage`, never decoded to
  numbers, so prices stay strings.
- **Full vs delta.** Default `delta`. `full` refuses a bare object (a paged or
  per-article source would replace the catalogue with one page per message)
  and an empty list (nothing is worth an import; an empty full import would
  wipe the tills if Bifrost let it through).
- **Abort on any failure.** begin, then items, then commit, with a deferred
  abort on every error after begin, run on a context detached from the
  delivery's (a cancelled delivery still abandons its import), best effort,
  404 and 204 both fine. No in-process retry of a single request: the whole
  import is retried by redelivery from a fresh begin (`AGENTS.md`: no own retry
  loop). Each batch carries `Idempotency-Key: <envelope id>-<n>` anyway.
- **Classification** as in acceptance criterion 3. A 2xx begin without an
  `import_id` is retriable (wrong base_url, a proxy page).
- **Rejected rows** in a 200 commit are logged (ten at most, with row,
  article_no, reason) and not retried: the same rows would be refused again.
- **Several nodes on a connection** each get the list. If any fails
  retriably the whole message is retried, and the permanent errors of the
  others ride along as plain text: a `Permanent` inside an `errors.Join` would
  make the SDK drop the message (`IsPermanent` uses `errors.As`).
- Delivery ceiling 20 min; per-request timeout 60 s.

## Decisions made without asking

- Type name `bifrost_catalogue` (underscore like `front_systems`), service
  `bifrost-catalogue-producer`, block `bifrost_catalogue`.
- Default `mode` is `delta`; `full` must be asked for.
- `allow_shrink_reason` is exposed, documented as "leave it out". Without it a
  deliberate shrink has no way through the guard.
- No `effective_from` in node config (one timestamp for every run makes no
  sense); an item may carry its own.
- A bare object in full mode and an empty list are Permanent, checked before
  any request.
- 401/403 are retriable (DLQ-kept), not Permanent, as #838 did for the other
  destinations. 404 is retriable.
- A commit answered 200 is success if its body starts with `{` (cut by the
  1 MiB read limit or not); any other body (a proxy page) is retriable. Items answers
  must carry a `total_so_far` equal to the cumulative count sent, else the
  import is abandoned and retried.
- 413 and 400 at items are retriable with a "lower batch_size" hint (only a
  proxy can cause them); 422 at items stays permanent.
- Ordering: a delayed retry of an older message can commit after a newer one
  and win. Needs a version check in Bifrost; documented, not solved here.
- `ALLOW_PRIVATE_UPSTREAMS` rules apply: a Bifrost on a private address (an
  in-cluster service) is refused like for every other connector. Bifrost has no
  TLS, so production needs a TLS front on a public or allowed address.

## Risks

- A begin whose answer is lost leaves an import the connector cannot name; it
  expires in Bifrost after an hour (cap 32 open, 429 meanwhile).
- The whole message is in memory (claim-check cap 128 MiB). Backlog items 208
  and 209.
- The item shape moves with Bifrost#249; the producer is shape-agnostic, so the
  converter in front of it owns that.

## Verification

`go build ./...`, `go vet ./...`, `golangci-lint run`, `go test -race` for the
package, `make lint-connector`, `make lint-tenant`, `go test ./pkg/managementapi`
(guard tests, `-count=1`). Mutation runs: no abort, 422 retriable, token
redaction off, permanent leaking in a mixed join, idempotency key dropped, full
accepting a bare object; each fails a test.

## Registration (done after #814 merged, in this PR)

Every file the new type touched. Differences from the first draft: the
connector refuses an inline `token` and resolves only `token_secret_id` (as
nats-consumer does for its credentials); `token` was already in the UI's
`SECRET_FIELDS`; there is no per-name Prometheus rule, only the scrape target;
`postgres-tls.sh` expects 33 connectors.

1. `infrastructure/azure/deploy-connectors-azure.sh`: in the `RETAIL` table add
   `bifrost-catalogue-producer producer -` (after `sitoo-producer producer -`).
2. `infrastructure/azure/affected.sh`: add `bifrost-catalogue-producer` to
   `CONNECTORS`. `build-push-acr.sh` has no list of its own (the guard
   `TestAffectedServicesKnowsEveryImage` keeps them equal); check it after the
   change.
3. `infrastructure/kubernetes/connectors/connectors.yaml`: a Deployment and
   Service `vrsky-bifrost-catalogue-producer`, copied from
   `vrsky-sitoo-producer` (role `producer`, image
   `vrskyprodacr.azurecr.io/vrsky/bifrost-catalogue-producer:latest`, same
   env: NATS, DATABASE_URL, ENCRYPTION_KEY, MinIO claim-check settings, health
   port 8080; no extra port). Hard limit: `needs-human`.
4. `src/pkg/managementapi/nodeconfig.go`: in `nodeConfigRules` add
   `{"producer", "bifrost_catalogue"}: {"bifrost-catalogue-producer", blockRequired("bifrost_catalogue")}`;
   and in `connector_urls.go` a `bifrost_catalogue` case calling a
   `connectorurl` check for `base_url` (https, no userinfo, same rule as
   `safeurl.CheckBaseURL`), plus the `nodeconfig_test.go` cases the existing
   rows have (rule table, URL validation).
5. `src/pkg/managementapi/schema_discovery_proxy.go`: nothing (producers have
   no sample data).
6. UI: `ui/src/types/nodeConfig.ts` (`'producer/bifrost_catalogue'` with block
   `bifrost_catalogue`, as `'producer/sitoo'`), the node palette and a
   `BifrostCatalogueConfigEditor` in `ui/src/components/Pipeline/PropertyEditor.tsx`
   (base URL, `SecretInput` for `token`, tenant id, mode select, batch size),
   `ui/src/utils/secrets.ts` (`token` is a secret field: check the list) and
   `ui/src/onboarding/templates.ts` if it lists connectors.
7. `docker-compose.yml`: a `bifrost-catalogue-producer` service next to
   `sitoo-producer` (build `src/cmd/bifrost-catalogue-producer/Dockerfile`,
   same environment).
8. `infrastructure/prometheus.yml` (or the scrape config): add the worker if
   workers are listed per name; check how `sitoo-producer` appears.
9. OpenAPI registry: nothing (no management-API route).
10. `docs/DEPLOYMENT_GUIDE.md`: add `bifrost-catalogue-producer` to the
    connectors row (line 31).
11. Mark `token` as a secret field so a raw token can't be stored on the node,
    only `token_secret_id`: `ui/src/utils/secrets.ts` and the management API's
    secret-field handling (find where `api_password` is listed and add
    `bifrost_catalogue.token` the same way).
12. `.github/workflows/`: nothing named per connector today (images come from
    `affected.sh`); confirm after step 2.
