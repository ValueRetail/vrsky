# Issue #643: a database error on the API-key lookup is not a rejected key

## Goal

`TenantAPIKeyMiddleware` and the API-key branch of `resolvePrincipalRole`
treat every `GetTenantByAPIKeyHash` error as "invalid API key". The
repository signals "no such key" with a plain string error, so a database
error and an unknown key look the same. A CI/CD client then sees a rejected
key for a database blip. Same split as #634 made for sessions: a key the
server has turned down is a 401, anything else is a 503.

## Acceptance criteria

- `GetTenantByAPIKeyHash` returns a sentinel, `ErrAPIKeyNotFound`, when no row
  matches. The query already filters on `is_active` and `tenants.deleted_at`,
  so a revoked or rotated-away key and a deleted tenant are "not found". Any
  other error is wrapped with `%w` and returned.
- `TenantAPIKeyMiddleware`: not found (or a nil tenant) answers 401 "invalid
  API key"; any other error is logged and answers 503 with a fixed body.
  A tenant that exists but is not `active` still answers 403, as today.
- `resolvePrincipalRole` (tenant-header RBAC): a not-found key stays an
  anonymous caller (401). Any other API-key lookup error is returned, so
  `RequireTenantRoleFromHeader` answers 503.
- `CreateConnectionRequest` resolves `target_api_key` through the same call:
  not found stays a 404, any other error is a 503 (today a 404 "no tenant
  found for this API key" for a database blip).
- No error text, key or hash in any response body or log line.
- No error path lets a request through.
- The new tests fail on main without the fix.

## Approach

- `errors.go`: `ErrAPIKeyNotFound`.
- `repo_data_sharing.go`: map `sql.ErrNoRows` to it, wrap the rest.
- `tenant_apikey_middleware.go`: `isAPIKeyRejection(err)` (is the sentinel)
  and `writeAPIKeyError(w, r, err)` mirroring `writeSessionError`: 401 for a
  rejection, otherwise `slog` the error with method and path (never the key
  or hash) and send 503 `sessionUnavailableMessage`.
- `rbac.go`, `data_connection_handler.go`: use the classifier.
- `writeSessionError` logs "session validation failed"; the RBAC path now
  reaches it for an API-key error too, so the log line becomes "credential
  lookup failed". The existing test that greps for the old text is updated
  to the new text (a log-text change, not a weakened assertion).
- The two repository test doubles that return `ErrTenantNotFound` for an
  unknown key now return `ErrAPIKeyNotFound`; the classifier accepts only the
  sentinel, so a double that returns something else gets a 503, the safe side.

### Left alone, and why

- Tenant status 403: the middleware answers 403 for an existing tenant that
  is not active, the RBAC branch treats it as no credential (401). Changing
  either is a contract change, not part of this bug.
- There is no key expiry in the schema (`tenant_api_keys` has `is_active`
  and `rotated_at` only; one row per tenant, rotation overwrites the hash).

## Files

- `src/pkg/managementapi/errors.go`, `repo_data_sharing.go`,
  `tenant_apikey_middleware.go`, `rbac.go`, `session_middleware.go`,
  `data_connection_handler.go`
- tests: `tenant_apikey_middleware_test.go` (unit),
  `tenant_apikey_middleware_db_test.go` (`testdb`), mock updates in
  `handler_test.go`, `rbac_test.go`, `session_middleware_test.go`
- `plans/backlog.md`, `docs/SECURITY.md`

## Risks

- Authentication code. A repository that signals not-found some other way
  would now give 503 instead of 401. Postgres does not; test doubles are
  updated.
- A cancelled request context is logged and answered 503 to nobody. Harmless.

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race ./pkg/managementapi/...`, `make lint-tenant`.
- New tests run with the fix reverted and fail.
- Mutation run on the classifier: always true (database-error tests fail),
  always false (the 401 tests fail).
