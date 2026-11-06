# #747: the security log misses password-reset completion, logout and email verification

Checked in the code on 2026-11-06 (`main` at `3b80cbcb`).

## What is there

`logAuthEvent` (`src/pkg/managementapi/auth_handler.go`) writes one row to
`auth_audit_log` per event: user id where known, email, event type, status,
reason, client address and user agent. Registration, login, reset requests,
password change, account deletion, OIDC sign-in and limit blocks write rows.
`ResetPassword`, `LogoutUser` and `VerifyEmail` write nothing, so a password
taken over through a reset link leaves no trace beyond the request.

## Change

Event names are the ones the table's migration comment already lists
(`000005_auth_system.up.sql`): `password_reset`, `logout`, `email_verified`,
with status `success` or `failed`, as `login` does.

| Handler | success | failed (reason) |
|---|---|---|
| `ResetPassword` | `password_reset` / `success` | `invalid token`, `token already used`, `token expired`, or the repository error |
| `LogoutUser` | `logout` / `success` | the error from invalidating it |
| `VerifyEmail` | `email_verified` / `success` | `invalid token`, `token already used`, `token expired`, or the repository error |

- The user and email come from looking the token up by its hash before it
  is spent: `GetPasswordResetToken`, `GetEmailVerificationToken`, then
  `GetUserByID`. An unknown token has no user,
  so its row has neither. The lookup is for the log only; the `Use…` calls
  still do the checks. One extra read per request on routes that are rare.
- Never in a row: the token, its hash, the new password or its hash, the
  session token. The reasons are fixed strings for the known errors.
- Rejected before a token is looked at (no body, no token, weak password,
  logout with no session token): no row, as for login's 400s.
- Repository interface unchanged.

### Review round 1 (PR #766)

- `logAuthEvent` makes client text fit the columns before the insert: email
  to 255 characters, user agent to 500 (Postgres counts characters), NUL
  removed, invalid UTF-8 replaced. A 501-character user agent used to make
  the insert fail and the event vanish. A failed insert now logs a warning
  with the event type, status and error, never the row.
- `LogoutUser` ends the session `SessionAuthMiddleware` put in the context
  instead of reading the token again. The middleware reads the cookie first,
  the handler read the header first, so a valid cookie plus a stale bearer
  header ended the wrong session and left the cookie's active. The route
  always has the middleware, so the `unknown session` row is gone (the
  middleware answers 401) and the unused `extractBearerToken` goes too.

## Files

- `src/pkg/managementapi/auth_handler.go`
- `src/pkg/managementapi/auth_events_db_test.go` (new, Postgres via `pkg/testdb`)
- `src/pkg/managementapi/auth_events_test.go` (new: lost-row warning, `auditText`)
- `docs/SECURITY.md`: what the security log records.

## Verify

- New DB tests fail on main (no rows) and pass here: each handler's success
  and failure rows, with user, email, address and user agent, and no column
  holding the token, its hash, the password or the session token.
- `go build`, `go vet`, `gofmt`, `golangci-lint`, `go test -race -count=1
  ./pkg/managementapi/...` with `VRSKY_TEST_POSTGRES_URL`, `make lint-tenant`,
  `make lint-connector`.
