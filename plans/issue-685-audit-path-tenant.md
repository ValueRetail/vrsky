# #685: audit platform plan and quota changes under the target tenant

## Problem

`AuditMiddleware` (`src/pkg/managementapi/audit_middleware.go`) sits outside
the route mux and files each entry under the tenant `TenantIDMiddleware` put
in the context, i.e. the caller's `X-Tenant-ID`. For the operator's writes to
another workspace that is wrong:

- `PUT /api/v1/platform/tenants/{tenant_id}/plan` (`platform.plan.set`) lands
  in the operator's own selected workspace, or nowhere when none is selected
  (#637 made the header optional there).
- `PUT /api/v1/tenants/{tenant_id}/plan` and `PUT /api/v1/tenants/{tenant_id}/quotas`
  are under `/api/v1/tenants`, which `TenantIDMiddleware` skips, so they are
  never audited at all.

The actor is missing too: the middleware reads the user from the outer
request, and `SessionAuthMiddleware` only adds it further in, per route.

## Acceptance criteria

- The three routes above are audited under the path's `tenant_id`, with the
  operator's user id and email as actor.
- With or without a workspace selected (context tenant).
- The operator's workspace gets no entry for them.
- Every other route audits under the context tenant, as before.
- The target tenant's `GET /api/v1/audit` lists the entry.
- Table-driven tests that fail without the change.

## Approach

Route metadata, smallest fit for the existing bag design:

- `auditBag` gains `tenantOverride` and `actor`.
- New wrapper `AuditUnderPathTenant(next)` sets both from
  `r.PathValue("tenant_id")` and the session user. The middleware uses the
  override when set, otherwise the context tenant, and the bag's actor when
  set, otherwise what it did before.
- `handler.go` mounts it on the three routes, inside `RequireOperator`, so
  only a caller already allowed to act on that tenant can file entries in its
  log. A refused non-operator keeps today's behaviour.

## Touches

- `src/pkg/managementapi/audit_middleware.go`
- `src/pkg/managementapi/handler.go` (three route lines)
- `src/pkg/managementapi/audit_target_tenant_test.go` (new)
- `docs/operator/billing.md`, `docs/guide/settings.md`
- `plans/backlog.md`: strike 155; new item for the missing actor on every
  other route (found here).

Not touched: `tenant_middleware.go`, `nodeconfig.go`, `visma_urls.go`
(batch PR #681), `cmd/management-api/cors.go`. No SQL changes, no OpenAPI
change (no response changes).

## Risks

- An operator call for a tenant id that does not exist files an orphan row
  under that id (no FK on `audit_log.tenant_id`); a non-UUID id fails the
  insert and logs a warning. Both are operator typos, and the request itself
  answers 404.
- Wrapper mounted before `RequireOperator` would let anyone write a 403 row
  into another workspace's log. A test pins the order.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./pkg/managementapi/...`;
`make lint-tenant` at the root. Tests run on the base commit must fail.
Mutations: ignore the override; prefer the context tenant; drop the actor;
mount the wrapper outside `RequireOperator`. Each must fail a case.
