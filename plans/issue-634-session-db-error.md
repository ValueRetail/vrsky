# Issue #634: a database error on the session lookup is not a rejected session

## Goal

`SessionAuthMiddleware` answers 401 "invalid or expired session" for every
`ValidateSession` error, a database error included. Since #611 the UI drops
its token on a 401 from `/auth/me`, so a database blip signs users out.
Only a session the server has actually rejected may answer 401. Anything else
answers 503.

## Acceptance criteria

- Unknown token, expired session, revoked (`is_active = false`) session and a
  session whose user is gone answer 401 on `/auth/me` and every other route
  behind `SessionAuthMiddleware`.
- Any other `ValidateSession` error (closed pool, cancelled context, lost
  connection) answers 503 with a fixed body that carries no error text, and is
  logged server-side with the error.
- A suspended user still gets 403 from `CanLogin`.
- `GetMe`, `ChangePassword` and `DeleteAccount` re-validate the session after
  the middleware did; they classify the same way. Today they send
  `err.Error()` as a 401 body, which leaks "failed to get session: <driver
  error>" on a database error.
- The tenant-header RBAC middleware (`resolvePrincipalRole`) answers 503 when
  the session lookup or the role lookup fails with a non-rejection error,
  instead of falling through to a 401.
- New tests fail on main without the fix.

## Approach

One classifier, `isSessionRejection(err)`, in `session_middleware.go`. An
error is a rejection when it matches one of:

- `auth.ErrSessionInvalid` (not found, revoked), `auth.ErrSessionExpired`,
  `auth.ErrUserNotFound` (the session's user is deleted);
- the "no token on the request" error `getAuthenticatedUser` already makes
  (now a named sentinel);
- `*NotFoundError` and `*UnauthorizedError`, the managementapi types the
  repository mocks return for an unknown session.

`PostgresRepository.ValidateSession` already returns exactly the three auth
sentinels for not found, revoked, expired and missing user, and wraps every
other error with `%w`, so no repository change is needed.

`writeSessionError(w, r, err)` is the one place that picks the response: 401
with the existing text for a rejection, otherwise log with `slog` and send 503
`ServiceUnavailable` "authentication service temporarily unavailable". The
middleware and the three handlers use it.

503 over 500: the request is fine, the dependency is down and a retry is
right. It also reads as "try again" to any client, and the UI already keeps
the token on any 5xx.

Fail closed: the classifier is an allow-list of rejections, but 503 never
authenticates anyone, so the only thing a wrong classification can do is show
a bad token a 503 instead of a 401. The user is not let in.

### Left alone, and why

- `OptionalSessionMiddleware`: not wired to any route. An error there leaves
  the request anonymous, which is the safe side. Noted, not changed.
- `TenantAPIKeyMiddleware`: tenant API keys, not sessions. The UI never calls
  it with a session, so no sign-out follows, and `GetTenantByAPIKeyHash` has
  no not-found sentinel (it returns a plain string error), so classifying
  would need a repository change. Follow-up in the backlog.
- No agent-token, SSE or websocket path calls `ValidateSession`; the SSE and
  stream fetches in the UI call `reportUnauthorized()` on 401, which asks
  `/me`, so they inherit the fix.
- `GetMe` swallows a `GetUserTenants` error and returns an empty tenant list.
  Different bug, noted in the backlog.

## Files

- `src/pkg/managementapi/session_middleware.go`
- `src/pkg/managementapi/auth_handler.go` (`GetMe`, `ChangePassword`,
  `DeleteAccount`, `getAuthenticatedUser`)
- `src/pkg/managementapi/rbac.go`
- `src/pkg/managementapi/session_middleware_test.go` (new, unit)
- `src/pkg/managementapi/session_middleware_db_test.go` (new, `testdb`)
- `plans/backlog.md`, `docs/SECURITY.md`

UI: `getMe` in `ui/src/services/authService.ts` clears the token only on 401
or 403 and `reportUnauthorized` only signs out when the token is gone, so a
503 keeps the session. No UI change.

## Risks

- Authentication code. A repository that returns some other error type for
  "not found" would now give 503 instead of 401. The Postgres repository does
  not; the test mocks use types the classifier lists.
- A cancelled request context (client went away) is logged and answered 503
  to nobody. Harmless.

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race ./pkg/managementapi/...`, `make lint-tenant`.
- New tests run against `main` (fix reverted) and fail; with the fix they
  pass.
- Mutation run on the classification: make `isSessionRejection` always true
  (database error tests must fail) and always false (the 401 tests must fail).
