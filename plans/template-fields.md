# Onboarding templates that match what the connectors read

Checked 2026-10-22: each of the seven templates in
`ui/src/onboarding/templates.ts` against the JSON keys its connectors
actually parse (`src/cmd/<connector>/*.go`), the backend's required-field
rules (`nodeConfigRules` in `src/pkg/managementapi/nodeconfig.go`) and the
builder's fields (`PropertyEditor.tsx`). The existing
`templates.test.ts` checks the catalog's shape (fields point at real
nodes, edges connect, secrets are masked), never that a key is one the
connector reads, which is how the drift went unnoticed.

## What is wrong

| Template | Problem | Effect |
|---|---|---|
| **API poll → File** | Writes `api.url`, `api.method`, `api.interval: "60s"`. api-consumer reads `base_url`, `endpoints[{path, params, auth_type, auth_value}]` and `poll_interval_seconds` (number), as the builder does. | Deploys, then polls nothing: no base URL, no endpoint, no interval. |
| **CSV → Database** | Source writes `file.format: "csv"` and no `file.path`; file-consumer reads `path` (the folder to watch) and has no format key. Destination writes `database.connection_string` and `operation: "insert"`; db-producer reads `host`, `port`, `user`, `password`, `database`, `table`, `sslmode`, `mode`, as the builder does. And nothing turns CSV into rows: file-consumer publishes the file's raw bytes, db-producer parses its payload as a JSON object or array. | **Refused at start**: the backend requires `file.path` and `database.host`. Fixed keys alone would still fail on the first file (CSV is not JSON). |
| **POS sales → ERP (Sitoo → BC)** and **ERP inventory → POS (BC → Sitoo)** | Keys all match. But the source's records go straight to the destination: a Sitoo transaction is not a Business Central sales order, and a BC item is not a Sitoo warehouse item. | Deploys and runs; every message is rejected by the target API and lands in the dead-letter queue. |
| Webhook → Slack, Webhook → HTTP, Webhook → File | Keys match (`http.url/method/headers`, `file.path`). | Fine. |

## Open questions

1. **The two retail templates.** They need a data-converter node with a
   real field mapping between them (Sitoo transaction → BC salesOrder
   lines; BC item → Sitoo warehouse item). Nobody has written or tested
   those mappings against the live APIs. Recommended: **take both out of
   the gallery for now** and bring them back with a tested mapping as its
   own task (the BC trial and a Sitoo sandbox are what it needs).
   Alternative: keep them with a converter node and a best-guess mapping,
   which would look finished and fail on the first real order.

   **Update 2026-10-28 (#435):** both are back in the gallery, marked
   "Beta: verify the mapping", as source → data-converter → destination.
   The mapping is written out in `plans/issue-435-sitoo-bc-mapping.md` and
   tested in Go against the real converter on the repo's Sitoo and BC
   fixtures. The converter gained what it lacked for this: an `each`
   mapping (receipt items → `salesInvoiceLines`) and `split_records` (one
   BC sales invoice per message), and the Sitoo and BC destinations now skip
   the raw source page a converter pipeline also delivers to them. Still
   open: a live run against the BC trial
   and a Sitoo sandbox, which is what the beta label stands for (backlog
   human item 9).
2. **CSV → Database: what turns a file into rows.** file-consumer
   publishes the whole file as raw bytes (checked: `service.go`,
   `ingestWatchedFile`); db-producer unmarshals its payload as a JSON
   array or object and inserts each object's fields as columns. The
   data-converter already converts CSV to JSON with headers
   (`input_format: "csv"`, `output_format: "json"`), no field mapping
   needed. Recommended: **add a data-converter node between them** and fix
   the keys (`file.path` with a default folder;
   `database.host/port/user/password/database/table`, `password` as the
   secret field, `mode: "insert"`), asking for host, port, user, password,
   database and table instead of one connection string. Alternative: drop
   the template too.
3. **A guard so this does not drift again.** Recommended: a vitest test
   that, for every template node, checks each config key against the keys
   the builder writes for that connector and the backend's required keys,
   from one table kept in `ui/src/types/nodeConfig.ts` beside the types
   that already describe node configs. Alternative: a Go test that parses
   `templates.ts`, which is brittle.

## What the build found (2026-10-22)

- **Numbers.** db-producer's `port` and api-consumer's
  `poll_interval_seconds` are Go ints; the wizard stored every field as a
  string, so a user typing a port or an interval would have produced a
  config the connector cannot unmarshal. Fields can now be marked
  `numeric`: the wizard stores a number, and keeps deploy disabled while
  such a field holds text. `CONNECTOR_KEYS` lists the numeric keys and the
  tests require the flag on exactly those.
- **A second template test** (`ui/tests/unit/onboarding.templates.test.ts`,
  older) also required `config.type` on every node; both now exempt
  converter nodes, which have no connector type and keep their settings
  flat.
- The wizard test that proved credentials go through `materializeSecrets`
  used the removed Sitoo template; it now uses *CSV → Database* (secret
  `password`).

## Approach

1. `templates.ts`: fix *API poll → File* to `base_url`, one endpoint
   `{path: '/', params: '', auth_type: 'none'}`, `poll_interval_seconds:
   60`; wizard fields: base URL, endpoint path, poll interval (seconds).
   Fix *CSV → Database* as in question 2 (three nodes: file → converter →
   database, two edges). Remove the two retail templates
   (question 1), keeping their definitions out of the catalog, not
   commented out.
2. `ui/src/types/nodeConfig.ts`: a small `CONNECTOR_KEYS` table for the
   connectors the templates use (webhook, http, file, api, database,
   data-converter, later sitoo and business_central): the keys each connector reads and
   which of them the backend requires.
3. `templates.test.ts`: every template node's keys are known for its
   connector, every required key is present or a wizard field, and every
   wizard field's key is known.

## Files

| File | Change |
|---|---|
| `ui/src/onboarding/templates.ts` | two fixed, two removed |
| `ui/src/types/nodeConfig.ts` | `CONNECTOR_KEYS` |
| `ui/src/onboarding/templates.test.ts` | the key checks |
| `ui/src/onboarding/OnboardingWizard.test.tsx` | if it names a removed template |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| template keys known per connector | no template writes a key its connector ignores | put `api.url` back |
| required keys present | the backend will not refuse a template at start | drop `file.path` from CSV → Database |
| wizard fields known | the user never fills a field that goes nowhere | label a field `connection_string` on the database node |

Then `npm run build`, `npx vitest run`, lint.

## Verify

On the local dev stack (never prod accounts): deploy *API poll → File*
against a public JSON endpoint and see files appear; deploy *CSV → Database*
against the dev Postgres and see rows. The three webhook templates are
unchanged.

## Non-goals

Writing the retail mappings; new templates; changing the builder.
