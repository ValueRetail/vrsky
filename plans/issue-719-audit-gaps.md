# #719: audit workspace creation; never file anonymous requests into a workspace

Backlog items 185 and 186. Follows #695/#697 (the per-request audit bag).

## Problem

1. `POST /api/v1/tenants` runs behind the session check only. The caller is
   recorded as actor, but no workspace is verified, so since #695
   `auditBag.tenant` returns "" and nothing is written. Sign-up
   (`POST /api/v1/auth/register`) creates a workspace too and is not in any
   workspace's log either.
2. A request with no valid credential falls back to the context tenant, the
   `X-Tenant-ID` header. Anyone can add rows (401s, 404s) to any
   workspace's audit log by sending its id.

## Decisions

- The created workspace counts as verified once the insert succeeded: the
  caller is its owner. `CreateTenantHandler` calls
  `recordAuditVerifiedTenant` with the new id, so `tenant.create` lands in
  the new workspace's log with the creator as actor and the workspace id as
  resource id. A refused or failed creation has no workspace and writes
  nothing.
- Sign-up gets the same entry: after the default workspace is created,
  `RegisterUser` records the new user as actor and the new workspace as
  verified, with action `tenant.create` and detail `via: registration`. If
  the workspace could not be created, nothing is written (as today).
- No unauthenticated request is written to a workspace's audit log.
  `auditBag.tenant` loses the context-tenant fallback and its parameter:
  path override, then verified workspace, then nothing. The workspace
  audit log is a record of what that workspace's members, key and operator
  did; an anonymous request is none of those.
- Sign-in and other account events already have their own log,
  `auth_audit_log`, written by `logAuthEvent` (registration, login success
  and failure, password reset requests, password change, account delete,
  OIDC sign-in). That stays as it is; nothing new is added there here.

## Acceptance criteria

- `POST /api/v1/tenants` by a signed-in user writes one entry under the new
  workspace: action `tenant.create`, resource `tenant`/new id, actor the
  creator, status 201. A workspace in the context does not take it.
- A refused creation (validation error, slug conflict) writes nothing.
- Sign-up writes one `tenant.create` entry under the new workspace, actor
  the new user, even with a workspace in the context.
- An anonymous request (no credential, a bad one, an inactive key) with any
  workspace in the context writes nothing: 401 on header routes, 401 on
  `/api/v1/tenants`, 404 on an unknown route, failed login, forgot-password,
  and a passing alerts webhook.
- #685/#695/#697 tests unchanged except the two that pinned the old
  anonymous fallback (`TestAudit_InactiveKeyIsNotRecorded`,
  `TestAuditBag_TenantPrecedence`), which now expect nothing. The middleware
  unit tests in `audit_test.go` put the tenant in the context with no
  credential; their stand-in handler now records it as verified, the way
  the real auth checks do.
- New tests fail on main.

## Files

- `src/pkg/managementapi/audit_middleware.go`
- `src/pkg/managementapi/tenant_handler.go`
- `src/pkg/managementapi/auth_handler.go` (`RegisterUser`)
- `src/pkg/managementapi/audit_actor_test.go`, `audit_test.go`,
  `audit_target_tenant_test.go` (`serveAudited` sends no header for an
  empty bearer), new
  `src/pkg/managementapi/audit_workspace_test.go`
- `src/cmd/management-api/main.go` (comment on the middleware order only)
- `docs/guide/settings.md`, `docs/llms-full.txt`, `plans/backlog.md`

## Risks

- Anonymous 401s stop showing up in workspace logs. Nobody could tell them
  apart from noise anyone could inject, so nothing of value is lost; failed
  sign-ins stay in `auth_audit_log`.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./pkg/managementapi/...
./cmd/management-api/...` with Postgres; `make lint-connector`,
`make lint-tenant`. Mutation: restore the fallback, drop each
`recordAuditVerifiedTenant` call; the new tests must fail.
