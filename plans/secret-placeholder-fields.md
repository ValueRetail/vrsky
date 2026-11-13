# Stored secrets only in credential fields (#853)

## The problem

`crypto.ResolveSecretsInJSON` expanded `{secret:<uuid>}` in every string of a
node config and turned every `<key>_secret_id` into `<key>`. Connectors then
validate their settings and quote values in errors (`auth_type %q is not one
of …`). An editor could put a placeholder, or a `auth_type_secret_id`, in an
enum-like setting and read any of the tenant's write-only secrets back
through Test Connection, schema discovery, the activity feed, a connection's
`last_error` or `X-Vrsky-Last-Error` in the DLQ, or have it sent to a target
in a header, body or filename. Reproduced on main for nats-consumer.

## Design

One definition, in `src/pkg/crypto/secretfields.go`: for each node config
`type`, the paths that may reference a stored secret. Paths are relative to
the node config (`sftp.password`, `tls.key`, `http.headers.*`), segments
match JSON keys case-insensitively (`strings.EqualFold`, the rule
encoding/json uses), `*` matches any one key or array index.

- `<field>_secret_id` is resolved only when `<field>` is on the list.
  Anywhere else the reference is dropped, not resolved, so the setting reads
  as if it were absent. That is what nats-consumer (#859) and
  bifrost-catalogue-producer (#840) already do, and it leaves a config
  stored before this change with a stray, unread reference working.
- A `{secret:` placeholder is expanded only where the list marks the path
  as taking one inline: HTTP header values only, which go to the target and
  nowhere else. On every other path, credential or not, it is refused.
  Credential settings take the `_secret_id` form, which is what the editor
  writes.
- The Azure connection string was inline in the first version. Review of
  #868 showed it is not all credential: `AccountName`, `BlobEndpoint` and
  `EndpointSuffix` become the service URL, and azblob transport and DNS
  errors quote it (`Get "http://127.0.0.1:1/<value>/b?…": connection
  refused` through schema discovery, `last_error`, `X-Vrsky-Last-Error`).
  It takes a reference only now; the editor writes
  `connection_string_secret_id`, and the docs never showed the inline form.
- Kafka's `ca_cert` is not on the list: a public PEM certificate, and the
  editor never writes a `_secret_id` for it.
- The node's type is read from the config itself. No `type`, a non-string
  one, or two keys that differ only in case (the connector could read
  either) means an empty list: every placeholder refused, every reference
  dropped.

API of `pkg/crypto`: `ResolveNodeSecrets` and `ResolveBlockSecrets` (for the
test and sample-data handlers, which get one settings block), and
`CheckNodeSecrets` / `CheckBlockSecrets` for the management API, which has
no secrets reader. The unrestricted `ResolveSecrets`,
`ResolveSecretsWithKey` and `ResolveSecretsInJSON` are removed, so nothing
can call them by mistake. Errors name the path, never a value.

Also from the issue's first comment: a missing secret now reads
`stored secret not found`, without the id and tenant.

### The list

| type | paths (`*` any key or index) | inline `{secret:}` |
|---|---|---|
| `http` | `http.headers.*` | yes |
| | `http.token`, `http.signature.secret`, `tls.client_ca`, `tls.cert`, `tls.key` | no |
| `api` | `api.endpoints.*.auth_value` | no |
| `database` | `database.password` | no |
| `sftp` | `sftp.password`, `sftp.private_key` | no |
| `kafka` | `kafka.password`, `kafka.client_cert`, `kafka.client_key` | no |
| `rabbitmq` | `rabbitmq.password` | no |
| `cloud_storage` | `cloud_storage.secret_access_key`, `cloud_storage.account_key`, `cloud_storage.connection_string`, `cloud_storage.credentials_json` | no |
| `sitoo` | `sitoo.api_password` | no |
| `front_systems` | `front_systems.subscription_key`, `front_systems.api_key` | no |
| `business_central` | `business_central.client_secret` | no |
| `visma` | `visma.client_secret` | no |
| `brightpearl` | `brightpearl.staff_token` | no |
| `sap_s4hana` | `sap_s4hana.password`, `sap_s4hana.client_secret` | no |
| `nats` | `nats.password`, `nats.creds`, `nats.nkey_seed`, `nats.ca_cert` | no |
| `bifrost_catalogue` | `bifrost_catalogue.token` | no |

Taken from the connector structs (`json:"…"` tags of the fields commented
"resolved from …_secret_id"), the UI's `SECRET_FIELDS` and SecretInput
fields, and the docs (connector pages, `docs/SECURITY.md` mTLS table). Every
documented use stays: refs on all of these, `{secret:}` in header values.
The DSN example in `docs/SECURITY.md` is gone: no DB connector reads a DSN
any more (host/user/password), and `migrate-secrets` only wrote placeholders
into `connection_string` DSNs of that old shape.

### Where it is enforced

1. Management API, save (`ValidateDAG`, create and update) and start
   (`ValidateNodeConfigs`): a placeholder outside the list is a
   `DAGValidationError` / `NodeConfigError` naming node and path.
   `POST /api/v1/connections/test` answers `ok:false` with the same text
   before contacting a worker; `POST /api/v1/schema-discovery/{source}`
   refuses with 400.
2. Every connector, at resolution: each caller of the old functions now
   calls `ResolveNodeSecrets` for its own node (or `ResolveBlockSecrets`
   for a block). A refusal comes out of the same call a failed secret read
   did, so it is handled the way that already was: producers return
   `sdk.ConfigUnavailable` (retried, then DLQ, #704/#824); consumers fail
   the start the way they fail it for an unreadable secret; the sample-data
   and test handlers answer `ok:false`. Consumers that resolved every node
   of the connection (sftp, cloud-storage, kafka, rabbitmq, db, api,
   webhook, and BC/Visma/Sitoo/Brightpearl/SAP) now resolve only their own
   node, so another connector's node can't fail them.
3. Sample-data handlers used to go on with the unresolved config when
   resolution failed. They now answer `ok:false` with the reason.

Belt and braces: with the list in place a non-credential setting never
holds a resolved value, so a validation error quoting one quotes what was
stored. No error or log in the connectors quotes a credential field
(checked with grep over `src/cmd` and `src/pkg`).

## Acceptance criteria

- A placeholder in any setting not on the list is refused at save, start,
  Test Connection and schema discovery, with a message naming the path.
- At runtime no connector resolves a placeholder or reference outside its
  list; a placeholder there is a config error, a reference is ignored.
- A test per connector family puts a placeholder (and a reference) in an
  enum-like setting and asserts the secret's value is absent from the
  error, status text, headers, body, logs and the test-endpoint answer.
- Every documented credential field still resolves.
- `docs/SECURITY.md` lists the allowed fields; connector pages, the HTTP
  page's header note and troubleshooting say what is refused.

## Touched

- `src/pkg/crypto` (resolver, new `secretfields.go`, tests)
- `src/pkg/managementapi` (validator, nodeconfig, test endpoint, schema
  discovery proxy, tests incl. a Postgres one)
- every `src/cmd/*` caller listed in the issue, plus api-consumer,
  db-consumer and webhook-consumer, which used `ResolveSecrets`
- `src/pkg/sdk/harness` (a shared case for producers)
- docs: `SECURITY.md`, `connectors/http.md`, `connectors/nats.md`,
  `connectors/bifrost-catalogue.md`, troubleshooting,
  `connectors/bifrost-sales-to-retailhero.md` (stale #850 sentence)

## Risks

- A stored config with a placeholder outside the list stops working (that
  is the point). A credential setting that held a whole-value placeholder
  (`"password": "{secret:…}"`) is refused too; it was never documented and
  the editor writes `password_secret_id`. Start says which setting.
- A setting I missed from the list would lose its secret. Every connector
  struct field fed from a `_secret_id` is on it, and each list entry has a
  resolve test.
- Residual, not fixed here: an editor can still point a credential setting
  at a host they control (an HTTP header, a DB host) and receive any of the
  tenant's secrets there. Secrets are tenant-wide, not bound to a
  connection. Added to `plans/backlog.md`, filed as #869.

## Verification

`go build`, `go vet`, `golangci-lint run`, `go test -race` on every touched
package, `make lint-tenant`, `make lint-connector`; the Postgres test with
a throwaway postgres:16 container; a mutation run that turns the check off
in `pkg/crypto` and confirms the tests fail.
