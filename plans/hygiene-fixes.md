# Hygiene PR: four small fixes found during this week's work

Each item was re-verified in the code on 2026-10-07; file:line references are
to `main` at `39e2ce4`.

## Open questions

1. **`CanLogin` messages.** After the timing fix, the only remaining way to
   tell "no such account" from "wrong password" is the message for accounts
   that *exist but cannot log in*: `account is suspended`, `email not
   verified` (`auth_models.go:100-111`). Recommended: **keep them** — a real
   user with an unverified email needs that hint, and it only confirms the
   existence of accounts that cannot be used anyway. Alternative: fold them
   into `invalid email or password` too.
2. **Tenant lint reach.** The linter only sees `QueryRowContext` /
   `QueryContext` / `ExecContext` calls with a backtick SQL string
   (`lint-tenant-filter/main.go:65`). The webhook consumer's three queries
   use plain `Exec` / `QueryRow`, so even pointing the linter at that
   directory would miss them. Recommended: **extend the regex** to the
   non-`Context` names as well (one line; no new violations elsewhere — the
   only other non-Context call in scope is the advisory lock in `dblock.go`,
   which touches no tenant table). Alternative: convert the three calls and
   leave the linter alone.
3. **The webhook consumer's `/sample-data/{id}`** (`server.go:216`) takes a
   bare connection id with no auth. It is **not** exposed by the ingress
   (only `/webhook` is, `webhooks-ingress.yaml:94,137`); the production UI
   uses the management API's tenant-scoped copy
   (`handler.go:725`), and the only caller of the consumer's own endpoint
   is the dev-only `fetch('http://localhost:9800/sample-data/')` in
   `PropertyEditor.tsx:1758`. Recommended: **keep it**, scope-tag it with a
   reason, and leave removal for when that dev path goes. Alternative:
   delete the endpoint and the dev fetch now.

## What the build found (2026-10-08)

Item 2 was smaller than planned: the SDK's publish closure
(`pkg/sdk/lifecycle.go`) already writes `last_payload` for every consumer,
tenant-scoped (`AND tenant_id = $3`) and throttled to one write per
connection per 30 s. The webhook consumer's own write was an unscoped
duplicate left from before the SDK took it over, so it is **removed** rather
than scoped. The webhook tests now pin the SDK's write down (`WithArgs(…,
connID, tenant)` + `ExpectationsWereMet`), and the mutation that must fail
them is dropping the tenant filter in the SDK. The same leftover write exists
in `api-consumer`, `db-consumer`, `file-consumer` and `tenant-consumer`
(bridge target) — a follow-up, not this PR. The preview can now be up to
30 s behind the last webhook, as it already is for every other source.

## Goal

Four defects that are each a few lines, one PR, no schema change:

| # | Defect | Fix |
|---|---|---|
| 1 | A tenant with no `tenant_quotas` row gets the table's `free` defaults (50 msg/s, 10 integrations, 1 GiB) instead of its plan's; and for a tenant id that does not exist the lookup recurses without bound | auto-create from `plan_limits` for the tenant's plan; one re-read, then an error |
| 2 | The webhook consumer writes `last_payload` by connection id only, and its queries are invisible to the tenant lint | tenant filter on the write; `*Context` + backticks; lint covers `cmd/webhook-consumer`; lint regex also catches non-`Context` calls |
| 3 | Login answers faster when the email is unknown (no bcrypt), which reveals who has an account | verify against a dummy hash when the user is not found |
| 4 | Everything the management API logs through `slog.Default()` — auth blocks, billing sweep, orchestrator failures — comes out as plain text while the rest is JSON | `slog.SetDefault(appLog)` in `main.go` |

## What exists (checked)

- `quotas.go:49-70` — `GetTenantQuotas`: on `ErrNoRows`, `INSERT INTO
  tenant_quotas (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING` with the
  error discarded, then **calls itself**. The row takes the column defaults
  from migration 000012 (`plan_name 'free'`, 50, 10, 1 GiB). `CreateTenant`
  (`repo_tenant.go:61`) and `SetTenantPlan` (`repo_billing.go:80`) already
  insert from `plan_limits`; this is the one path that does not. Every live
  tenant has `subscription_plan` in `trial|paid|enterprise` after
  migration 000026. Callers: integration-count quota check
  (`handler.go:189`), quotas page (`quotas_handler.go:25`), test generator.
- `webhook-consumer/server.go:205` — `UPDATE connections SET last_payload =
  $1 WHERE id = $2` with `ac.TenantID` in hand; `:235` PK read; `:237`
  same-tenant fallback (derives the tenant from the connection row, so it
  cannot cross tenants). `src/Makefile:60-64` runs the linter on
  `pkg/managementapi` and `cmd/remote-agent` only.
- `auth_handler.go:232-252` — unknown email returns 401 immediately; a
  known email goes through `auth.VerifyPassword` (bcrypt cost 12, ~250 ms).
  The rate limiter already treats both the same; the timing does not.
- `cmd/management-api/main.go:51-52` — `appLog := logging.New(...)` (JSON
  handler with `service=management-api`) is threaded by hand; `slog.Default()`
  is never set, so 25 call sites across `billing_handler.go`,
  `auth_limit.go`, `handler.go`, `notifications_handler.go`,
  `nats_instances_handler.go`, `oauth_handler.go` and `main.go` itself write
  the text handler's format to stderr.

## Approach

### 1. Quotas follow the plan

```go
// GetTenantQuotas: on ErrNoRows
_, err = r.db.ExecContext(ctx, `
    INSERT INTO tenant_quotas (tenant_id, plan_name, max_msg_per_sec, max_integrations, max_storage_bytes)
    SELECT t.id, p.plan_name, p.max_msg_per_sec, p.max_integrations, p.max_storage_bytes
      FROM tenants t
      JOIN plan_limits p ON p.plan_name = COALESCE(
             (SELECT plan_name FROM plan_limits WHERE plan_name = t.subscription_plan), 'trial')
     WHERE t.id = $1
    ON CONFLICT (tenant_id) DO NOTHING`, tenantID)
```

then one plain re-read via a private `readTenantQuotas`; if that still finds
no row (the tenant does not exist) return its `sql.ErrNoRows` wrapped. No
recursion. A plan name with no `plan_limits` row falls back to trial limits
rather than to the table defaults.

### 2. Webhook consumer under the tenant lint

- `server.go:205` → `s.db.ExecContext(r.Context(), \`UPDATE connections SET
  last_payload = $1 WHERE id = $2 AND tenant_id = $3\`, data,
  ac.ConnectionID, ac.TenantID)`.
- `server.go:235,237` → `QueryRowContext` with backtick SQL. The PK read
  gets `// lint:tenant-ok — cluster-internal endpoint; the row is the scope`
  and the fallback already compares `c2.tenant_id = c1.tenant_id`.
- `lint-tenant-filter/main.go:65`: `(?:QueryRow|Query|Exec)(?:Context)?\s*\(`.
  Run the linter on both existing roots first to confirm zero new
  violations before committing the regex change.
- `src/Makefile`: add `LINT_ROOT=cmd/webhook-consumer $(GO) run
  ./cmd/lint-tenant-filter` with a comment (it writes `last_payload` and
  serves `/sample-data`).

### 3. Login takes the same time for unknown emails

```go
// auth_handler.go
var loginDummyHash = sync.OnceValue(func() string {
    h, _ := auth.HashPassword(randomPassword()) // 32 random bytes, hex
    return h
})
```

In `LoginUser`, when `GetUserByEmail` fails: `_ = h.verifyPassword(loginDummyHash(),
req.Password)` before the 401. `verifyPassword` is a Handler field defaulting
to `auth.VerifyPassword` (set in `NewHandler`; `nil` falls back) so a test can
record calls. The dummy hash is computed once per process, on first use.

### 4. JSON everywhere

`main.go:52`: `slog.SetDefault(appLog)` right after `logging.New`. This also
routes the std `log` package through the JSON handler. No call-site changes.

## Files

| File | Change |
|---|---|
| `src/pkg/managementapi/quotas.go` | plan-aware auto-create, no recursion |
| `src/pkg/managementapi/quotas_db_test.go` (new) | the two DB tests below |
| `src/cmd/webhook-consumer/server.go` | tenant filter, `*Context`, backticks, one `lint:tenant-ok` |
| `src/cmd/webhook-consumer/consumer_test.go` | `WithArgs(…, connID, tenantID)` on the two `last_payload` expectations |
| `src/cmd/lint-tenant-filter/main.go` | regex covers non-`Context` calls |
| `src/Makefile` | third `LINT_ROOT` |
| `src/pkg/managementapi/auth_handler.go` | dummy-hash verify, `verifyPassword` seam |
| `src/pkg/managementapi/handler.go` | `verifyPassword` field + default in `NewHandler` |
| `src/pkg/managementapi/auth_limit_test.go` | `TestLogin_UnknownEmailStillVerifiesAPassword` |
| `src/cmd/management-api/main.go` | `slog.SetDefault(appLog)` |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestManagementAPIUsesJSONDefaultLogger` source guard (same style as the CI and restore guards there) |
| `docs/operator/troubleshooting.md` | one line: management-api logs are JSON throughout |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestQuotasDB_AutoCreateFollowsThePlan` (Postgres): create tenant, delete its quota row, set `subscription_plan='paid'`, `GetTenantQuotas` → 200 / 20 / 100 GiB, `plan_name='paid'` | the fix | revert to the bare insert → gets 50 / 10 / `free` |
| `TestQuotasDB_UnknownTenantErrorsInsteadOfRecursing`: random uuid, 2 s timeout → error, no hang | the recursion is gone | restore the self-call → times out |
| `TestWebhook…` existing two, with `WithArgs(sqlmock.AnyArg(), connID, tenantID)` | the write is tenant-scoped | drop `AND tenant_id = $3` → sqlmock arg mismatch, and `make lint-tenant` fails |
| `TestLogin_UnknownEmailStillVerifiesAPassword`: recorder seam; unknown email → one verify call with a bcrypt hash that is not any user's; known email with wrong password → one call with the user's hash | the oracle is closed | remove the dummy verify → zero calls |
| `TestManagementAPIUsesJSONDefaultLogger`: `main.go` contains `slog.SetDefault(appLog)` after `logging.New` | the one line stays | delete it |
| Lint self-check: run the extended linter on a scratch file with `db.Exec(\`UPDATE connections SET x=1 WHERE id=$1\`)` → one violation | the regex change works | — (manual, output pasted in the PR) |

Then `gofmt`, `go vet`, `golangci-lint`, `make lint-tenant` (three roots),
`go test -race ./...` with `VRSKY_TEST_POSTGRES_URL`.

## Verify in prod (after deploy, on your word)

1. `kubectl logs deploy/vrsky-management-api` after the restart: every line
   JSON (the `Starting management-api` line included).
2. Ten wrong passwords for an unknown email from my address → the
   `Sign-in attempts blocked` line arrives as JSON with `service`,
   `endpoint`, `scope`.
3. `time curl` login with an unknown vs a known email: both ~250 ms+.
4. psql: `SELECT count(*) FROM tenants t LEFT JOIN tenant_quotas q ON
   q.tenant_id = t.id WHERE q.tenant_id IS NULL` → 0 (it is 0 today after
   the hand insert; the fix is for the next one).

## Rollout

`az acr build` management-api (with `AGENT_VERSION`) and webhook-consumer;
`deploy-core-azure.sh management-api`; `kubectl rollout restart
deploy/vrsky-webhook-consumer` — which now restores the running webhook
pipeline by itself (#306), so no redeploy afterwards. No env, no migration.

## Risks

- **`slog.SetDefault` changes the std `log` output format** for any library
  logging through `log.Printf` (pgx does not; nats.go uses callbacks). Only
  the format changes, never the destination.
- **Dummy bcrypt on every unknown-email attempt** costs ~250 ms of CPU per
  request — the same as a wrong password today, and the address limiter
  caps it at 10 per minute per address.
- **The lint regex** now also matches `Query(` inside non-SQL method names
  only if followed by a backtick string; confirmed zero new hits on the two
  existing roots before the change is committed.

## Non-goals

Sign-up email enumeration (`RegisterUser` says "already registered"),
forgot-password sending nothing, change-password guessing — separate items
on the list. Moving the dev-only `/sample-data/` fetch in `PropertyEditor`
to the management API endpoint.
