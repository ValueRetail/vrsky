# Open sign-up, paid plans: trial → request → you activate

## Open questions

1. **Trial length.** Recommended: **14 days** from sign-up. Alternative: 30.
2. **Existing workspaces** (yours, the BC/Bifrost tenants). Recommended:
   the migration marks every tenant that exists today as **paid**, so
   nothing you run stops. New sign-ups start on trial.
3. **Who is the operator** (the person who activates plans). Recommended:
   `PLATFORM_OPERATORS` env on the management API — a comma-separated list of
   user emails, i.e. yours. Alternative: a column on `users`; more to build,
   same effect.
4. **Trial and paid limits.** Recommended (from `docs/pricing-proposal.md`,
   collapsed to the two self-serve tiers you chose):

   | Plan | Integrations | Throughput | Storage | Messages/month |
   |---|---|---|---|---|
   | trial | 2 | 25 msg/s | 1 GiB | 100 k (shown, not enforced today) |
   | paid | 20 | 200 msg/s | 100 GiB | 10 M |
   | enterprise | unlimited | unlimited | unlimited | — |

   Prices are not in the product: the plan page says "pricing on request"
   and you quote. Tell me if you want a number shown instead.
5. **Tell you in Teams** when a plan is requested and when a trial expires.
   Recommended: yes, through the platform notification targets that
   already exist — these need a human, unlike the successes you muted.

## Goal

Anyone can sign up and gets a working workspace for 14 days. When the trial
ends, their pipelines stop and the UI says why; everything they built is
kept. They ask for a plan from inside VRSky; you hear about it in Teams,
invoice them, and activate the plan from an operator page. Their pipelines
come back. Nobody can give themselves a plan or raise their own limits.

Payments stay outside the product (invoice). The states and tables are laid
out so a Stripe webhook can set the same fields later.

## What exists (checked 2026-10-07)

- `tenants.subscription_plan` (default `free`; `validPlans` = free/pro/
  enterprise, which only feeds the dev-stack Traefik limits — not used on
  AKS) and `tenant_quotas` (`plan_name` + three enforced ceilings:
  `max_msg_per_sec`, `max_integrations`, `max_storage_bytes`). A new tenant
  gets a default `free` quota row the first time it is read.
- **Owners set their own plan and quotas**: `PUT /tenants/{id}/plan` and
  `PUT /tenants/{id}/quotas` are `ownerMW`, and the Usage page has the form.
  That has to go: with paid plans, limits are the product.
- No operator role of any kind. No trial, no expiry, no billing state.
- Usage metering (`usage_daily`) and the Usage page are in place; monthly
  message allotment is shown, not enforced (unchanged here).
- Alerts fan out to tenant targets or, without a tenant, to targets flagged
  `platform` (`dispatchAlert`). Teams is one of them.
- Hourly jobs run advisory-lock-gated so one of the two replicas does the
  work (`NewUsageRollup`, `NATSHealthMonitor`).
- Stopping a pipeline = `orchestratorFactory(conn).StopPipeline` +
  `UpdateConnectionStatus(stopped)`; starting is `StartConnection`.
- `RegisterUser` creates user + tenant + auto-verifies, open to the internet.

## Approach

### 1. Model — migration `000026_paid_plans`

```sql
ALTER TABLE tenants
  ADD COLUMN billing_status VARCHAR(16) NOT NULL DEFAULT 'trial'
      CHECK (billing_status IN ('trial','paid','suspended')),
  ADD COLUMN trial_ends_at  TIMESTAMPTZ,
  ADD COLUMN billing_note   TEXT;                       -- operator's own note
UPDATE tenants SET billing_status = 'paid', subscription_plan = 'paid';  -- question 2
ALTER TABLE tenants ALTER COLUMN subscription_plan SET DEFAULT 'trial';

CREATE TABLE plan_limits (                              -- the tiers, question 4
  plan_name VARCHAR(32) PRIMARY KEY, max_msg_per_sec INT, max_integrations INT,
  max_storage_bytes BIGINT, included_messages_per_month BIGINT, sort_order INT);
INSERT trial / paid / enterprise …

CREATE TABLE plan_requests (                            -- the operator's inbox
  id UUID PK, tenant_id UUID REFERENCES tenants ON DELETE CASCADE,
  requested_plan VARCHAR(32), message TEXT, requested_by UUID REFERENCES users,
  created_at TIMESTAMPTZ, handled_at TIMESTAMPTZ, handled_by UUID, outcome VARCHAR(32));

ALTER TABLE connections ADD COLUMN stopped_by_billing BOOLEAN NOT NULL DEFAULT false;
```

`tenant_quotas.plan_name` follows `subscription_plan`; setting a plan copies
that plan's `plan_limits` row into the tenant's quotas (the operator may still
override one tenant's numbers afterwards). Both tables go into the tenant
lint's scoped list.

### 2. Sign-up

`RegisterUser` → tenant with `billing_status = trial`, `subscription_plan =
trial`, `trial_ends_at = now() + 14 days`, quotas from `plan_limits('trial')`.
Response carries the trial end so the UI can say "your 14-day trial has
started".

### 3. The operator

`operatorMW`: the session user's email is in `PLATFORM_OPERATORS`. 403
otherwise, and the UI never shows the page to anyone else (`/auth/me` gains
`is_platform_operator`). Routes, all audited:

| Route | Does |
|---|---|
| `GET /api/v1/platform/tenants` | every workspace: owner, plan, billing status, trial end, open request, messages last 30 d |
| `PUT /api/v1/platform/tenants/{id}/plan` | `{plan, trial_ends_at?, note?}` → sets plan + limits, `paid`/`trial`, closes open requests as `accepted`; **pipelines the lapse stopped are started again** |
| `GET /api/v1/platform/plan-requests` | open requests |

`PUT /tenants/{id}/plan` and `PUT /tenants/{id}/quotas` move behind
`operatorMW` (owners lose them). The platform list is read across tenants on
purpose — `lint:tenant-ok` with the operator check named in the comment.

### 4. The customer

| Route | Does |
|---|---|
| `GET /api/v1/tenants/{id}/billing` | plan, status, trial end, limits, open request (any member) |
| `POST /api/v1/tenants/{id}/plan-requests` | owner: `{plan: paid\|enterprise, message}` → row + platform alert `PlanRequested` → Teams |

### 5. Trial expiry and the gate

- **Sweep**, hourly and at start, advisory-lock-gated like the rollup:
  tenants with `trial` and `trial_ends_at < now()` → `suspended`; each
  running connection is stopped (`StopPipeline`, status `stopped`,
  `stopped_by_billing = true`, a connection event "stopped: trial ended");
  audit row; platform alert `TrialExpired` → Teams.
- **Gate** while `suspended`: `StartConnection`, `CreateConnection`, the
  tenant-data ingest (`/api/v1/data/…`) and connection requests answer
  **`402 PlanRequired`** with the request route in the body. Reading,
  editing, secrets, users, exporting usage all keep working.
- `paid` tenants have no `trial_ends_at`; `enterprise` is `paid` with the
  unlimited row.

### 6. UI

- **Banner** under the header (`RootLayout`): trial → "Trial · N days left ·
  Choose a plan"; suspended → "Your trial has ended — pipelines are stopped.
  Request a plan". Hidden when paid.
- **Settings → Plan** (`/settings/plan`): current plan, trial end, limits,
  "Request the Paid plan" / "Talk to us about Enterprise" with a message
  box; after sending: "Requested on … — we'll be in touch".
- **Usage page**: the quota form goes for everyone but operators; owners see
  limits read-only with a link to Plan.
- **Platform → Workspaces** (`/platform/tenants`, operators only): the table
  from §3 with a plan select, trial-end date and note per row; one Save per
  row. Open requests at the top.
- `402` handling in `api.ts`: toast "Your workspace needs a plan" + link,
  like the session-expiry handling.

### 7. Docs

`docs/pricing-proposal.md` → what was decided; `docs/operator/billing.md`
(new): the flow, the operator page, `PLATFORM_OPERATORS`, how to extend a
trial, how to grandfather; troubleshooting entry "pipelines will not start:
402".

## Files

| Area | Files |
|---|---|
| Migration | `infrastructure/migrations/000026_paid_plans.{up,down}.sql` |
| Model/repo | `tenant_models.go`, `repo_tenant.go`, `quotas.go` (+`plan_limits`), `repo_plan_requests.go` (new), `repository.go`, `handler_test.go` (mock) |
| Operator | `operator_middleware.go` (new), `platform_handler.go` (new), `plan_handler.go`, `quotas_handler.go`, `handler.go` (routes), `openapi_registry.go`, `cmd/management-api/{main,config}.go` |
| Customer | `billing_handler.go` (new), `auth_handler.go` (sign-up trial), `auth_models.go` (`is_platform_operator`) |
| Enforcement | `billing_sweep.go` (new), gates in `handler.go` (`CreateConnection`, `StartConnection`), `tenant_data_handler.go`, `data_connection_handler.go` |
| Alerts | `notify` names `PlanRequested`, `TrialExpired` |
| Lint | `cmd/lint-tenant-filter/main.go` (two tables) |
| UI | `components/Layout/{RootLayout,PlanBanner}.tsx`, `pages/PlanPage.tsx`, `pages/PlatformTenantsPage.tsx`, `pages/UsagePage.tsx`, `services/{billingService,platformService}.ts`, `services/api.ts`, `App.tsx`, `Sidebar.tsx`, `types/models.ts` (+tests) |
| Docs | `docs/pricing-proposal.md`, `docs/operator/billing.md`, `docs/operator/troubleshooting.md` |

Not changed: connectors, the gateway, NATS, Stripe (none), the monthly
message cap (still shown, not enforced).

## Tests

| Test | Proves | Mutation |
|---|---|---|
| `TestSignup_StartsATrial` | new tenant: trial, ends in 14 d, trial limits in `tenant_quotas` | default plan left `free` |
| `TestPlan_OwnerCannotSetPlanOrQuotas` | owner → 403 on both routes; operator → 200 | keep `ownerMW` |
| `TestOperator_IsByEmailListOnly` | unlisted admin 403; listed user 200; empty list → nobody | match on role |
| `TestOperator_SetPlanCopiesLimitsAndRestartsStoppedPipelines` | quotas follow `plan_limits`; `stopped_by_billing` connections start; others untouched; request closed | skip the restart |
| `TestSweep_SuspendsExpiredTrialsAndStopsPipelines` (Postgres) | only expired trials; running connections stopped and flagged; paid untouched; alert dispatched once; idempotent on rerun | drop the `trial_ends_at` condition |
| `TestGate_SuspendedTenant402` | start/create/ingest → 402 with the request route; reads still 200 | gate only start |
| `TestPlanRequest_OwnerOnlyAndNotifiesPlatform` | row + platform alert; editor → 403; a request for `trial` is 400 | notify the tenant's targets instead |
| `TestIsolation_BillingAndRequestsAreTenantScoped` | tenant B cannot read A's billing or requests | drop `tenant_id` filter → also `lint-tenant` fails |
| `TestPlatformList_RequiresOperator` | 403 for an owner; rows for an operator | — |
| Migration (Postgres) | existing tenants come out `paid`; `plan_limits` seeded; down works | — |
| UI | banner per state; Plan page request flow; 402 toast; Platform page hidden without the flag, Save calls the API | — |

Then `gofmt`, `go vet`, `golangci-lint`, `lint-tenant`, `lint-openapi`,
`go test -race ./...` with the database, UI `tsc` + coverage gate.

## Rollout (prod, on your word)

1. Set `PLATFORM_OPERATORS=<your email>` on the management-api deployment
   (manifest + `kubectl set env`).
2. `build-push-acr.sh core` → `deploy-core-azure.sh management-api ui`. The
   migration runs on start and grandfathers today's workspaces as paid.
3. Check: your workspaces show no banner; `/platform/tenants` lists them as
   paid; a throwaway sign-up shows the trial banner and the Plan page;
   requesting a plan produces a Teams card; setting it `paid` on the
   platform page clears the banner. Set its `trial_ends_at` to yesterday,
   run the sweep (next tick or restart) → banner turns red, its pipeline
   stops, a Teams card arrives.

## Risks

- **Sign-up is still unauthenticated**, now rate-limited (#302). A trial
  workspace costs a NATS account provisioning; worth watching
  `tenants` growth the first weeks.
- **Suspension stops live pipelines** on the hour after the trial ends; a
  customer mid-evaluation is interrupted. The banner counts down from day
  one, and you can extend the trial from the platform page.
- **Operator by email list**: a typo locks you out of the platform page
  (nothing else); fix is `kubectl set env`.
- **Grandfathering is a one-off UPDATE in the migration**; a later rollback
  (`down`) drops the columns and loses billing state — acceptable before the
  first customer, not after.
- `402` is unusual; browsers and proxies pass it through. The UI handles it
  explicitly.

## Non-goals

Stripe or any payment integration; enforcing the monthly message allotment;
overage billing; invoices or receipts in the product; email to customers
(prod has no SMTP — the Plan page and Teams carry the conversation);
closing sign-up; a public pricing page.
