# #695: record the actor on every audit entry; audit owner writes under /tenants

Backlog items 168 and 169, found in #685.

## Problem

`AuditMiddleware` wraps the route mux, so it never sees what the per-route
middleware put on the inner request context:

1. The principal. `SessionAuthMiddleware`, `resolvePrincipalRole` (behind
   `RequireTenantRoleFromHeader`) and `TenantAPIKeyMiddleware` add the user or
   the key's tenant further in. `buildAuditEntry` falls back to
   `GetUserFromContext(r.Context())` on the outer request, which is always
   empty, so `user_id` and `actor_label` are empty on every entry except the
   three operator routes #685 fixed.
2. The tenant for `/api/v1/tenants/{tenant_id}/*`. `TenantIDMiddleware` skips
   those paths and `TenantMemberMiddleware` sets the tenant on the inner
   context only, so plan requests, members, invites and API-key rotation are
   never audited.

## Acceptance criteria

- Every audited request that authenticated has an actor: a session user as
  `actor_kind` `user`, `user_id` and email; an API key as `actor_kind`
  `api_key`, no `user_id`, and a label naming the key's workspace. Never the
  key or its hash.
- Owner writes under `/api/v1/tenants/{tenant_id}/*` are filed under that
  tenant, with no workspace selected (as in production).
- The tenant is recorded only after `TenantMemberMiddleware`'s membership
  check passes: a non-member's refused request writes nothing into the
  target tenant.
- `AuditUnderPathTenant` still wins over everything else; the three operator
  routes from #685 behave as before.
- No other route's audit tenant changes.
- `docs/operator/billing.md` (`plan.request`) and the audit section of
  `docs/guide/settings.md` / `docs/llms-full.txt` are true.

## Approach

All in the existing per-request bag (`audit_middleware.go`):

- `auditBag.actor *User` becomes `actor *auditActor{kind, userID, label}`;
  new `memberTenant string`.
- Helpers, no-ops without a bag: `recordAuditUser(ctx, *User)`,
  `recordAuditAPIKey(ctx, *Tenant)`, `recordAuditMemberTenant(ctx, id)`.
- Callers:
  - `SessionAuthMiddleware`: after the session validates and `CanLogin`
    passes.
  - `resolvePrincipalRole`: on a valid session, and on a valid active key.
  - `TenantAPIKeyMiddleware`: after the key validates and its tenant is
    active.
  - `TenantMemberMiddleware`: right before `next`, after the membership
    check and the tenant lookup.
- `auditBag.tenant`: path override, then member tenant, then context tenant.
- `AuditUnderPathTenant` keeps setting the override only; the actor now
  comes from the session middleware.
- `buildAuditEntry` uses the bag's actor only; the dead
  `GetUserFromContext(r.Context())` fallback goes.

Decision: the actor is recorded when the credential is verified, before the
route's authorization. "Who tried" is what a log of refused writes is for,
and the tenant an entry is filed under does not depend on it. The API-key
label is `API key (<workspace slug>)`: keys have no name of their own (one
per workspace, `tenant_api_keys` has no label column).

## Touches

- `src/pkg/managementapi/audit_middleware.go`
- `src/pkg/managementapi/session_middleware.go`
- `src/pkg/managementapi/rbac.go`
- `src/pkg/managementapi/tenant_apikey_middleware.go`
- `src/pkg/managementapi/tenant_middleware.go`
- `src/pkg/managementapi/audit_actor_test.go` (new),
  `audit_target_tenant_test.go` (the plan-request case that documented
  backlog 169 now expects the path tenant)
- `docs/operator/billing.md`, `docs/guide/settings.md`, `docs/llms-full.txt`
- `plans/backlog.md`: strike 168 and 169

No SQL, no migration, no OpenAPI or response change. `handler.go` unchanged.

## Risks

- Recording the tenant before the membership check would let anyone file
  entries into any workspace's log. Tests pin it; a mutation moves it.
- A forged `X-Tenant-ID` on a header route was already filed under that
  workspace (unchanged); it now carries the caller's identity too.
- Unauthenticated requests keep today's empty actor (`actor_kind` `user`).

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./pkg/managementapi/...
./cmd/management-api/...` with Postgres; `make lint-tenant`. New tests fail
on main. Mutations: record the tenant before the membership check; member
tenant over the path override; context tenant over member tenant; drop each
actor call; put the raw key in the label.
