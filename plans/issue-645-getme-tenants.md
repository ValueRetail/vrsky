# Issue #645: GetMe hides a failed tenant lookup

## Goal

`GetMe` (`GET /api/v1/auth/me`) turns a `GetUserTenants` error into an empty
tenant list and a 200. A database blip right after the session check shows a
signed-in user with no workspaces. Answer 503, the way #634 does for the
session lookup, and log the error.

## Acceptance criteria

- A `GetUserTenants` error answers 503 `ServiceUnavailable` with the fixed
  body #634 uses (`sessionUnavailableMessage`). The error text is not in the
  body, and the error is logged with method and path.
- A successful lookup with zero tenants (or a nil slice) is still a 200 with
  `"tenants": []` and `"current_tenant": null`.
- A successful lookup with tenants is still a 200 with the list and
  `current_tenant` set to the first one.
- The new error case fails on main without the fix.

## Approach

In `GetMe` (`src/pkg/managementapi/auth_handler.go`), on a `GetUserTenants`
error log with `slog.Default().Error` and answer through the same 503 body as
`writeSessionError`. `writeSessionError` itself classifies a session
rejection first, which a tenant error is not, so the 503 write is pulled out
of it into `writeServiceUnavailable(w, r, msg, err)` and both callers use it.
The log line differs ("tenant lookup failed" vs "session validation
failed"); the response is identical.

503 over 500: the request is fine and the dependency is down, so a retry is
right. It is a 5xx, so the UI keeps the token.

## Files

- `src/pkg/managementapi/auth_handler.go` (`GetMe`)
- `src/pkg/managementapi/session_middleware.go` (small helper split)
- `src/pkg/managementapi/auth_me_tenants_test.go` (new)
- `plans/backlog.md` (strike 149, add 150)

Not touched: `TenantAPIKeyMiddleware` and the API-key repository code (#643).

## UI check (no change needed)

`getMe` in `ui/src/services/authService.ts` clears the token only on 401/403
and returns null on anything else. `checkAuth` in `ui/src/store/authStore.ts`
then keeps a cached user and shows "Could not refresh your account", or, with
no user, sets `loadFailed` and `ProtectedRoute` shows the retry screen. So a
503 reads as "error, retry", not as "no workspaces". A real empty list is
still a 200 with `tenants: []`, which is the only path to the "no
workspaces" state.

## Other handlers that list tenants

Looked at every `GetUserTenants` caller and the other tenant lookups:

- `ListTenantsHandler` (`GET /api/v1/tenants`): answers 500 on error, does not
  hide it. Left alone.
- `HandlePlatformListTenants`: 500 on error. Left alone.
- `TenantMemberMiddleware`: a `GetTenantByID` error answers 404 "tenant not
  found", so a database error looks like a missing workspace. Same shape of
  problem. Not fixed here; backlog item 150.

## Risks

Small. A user whose tenant lookup keeps failing now gets the retry screen
instead of an empty workspace list. That is the intent.

## Verify

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./pkg/managementapi/...`, `make lint-tenant` from `src/`.
Tests run against the pre-fix code to confirm the error case fails.
