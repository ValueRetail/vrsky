# Issue #662: TenantMemberMiddleware turns a failed tenant read into a 404

## Goal

After the membership check, `TenantMemberMiddleware` answers 404 "tenant not
found" for any `GetTenantByID` error, so a database blip on a tenant route
looks like a deleted workspace. The membership lookup above it answers 500 for
a database error. Only a real not-found should be a 404; everything else is a
503 like #634, #643 and #645.

## Acceptance criteria

- `GetTenantByID` returns `ErrTenantNotFound` for a missing row
  (`PostgresRepository`), so that, and `sql.ErrNoRows` (also via wrapping), stay
  a 404 "tenant not found".
- Any other `GetTenantByID` error answers 503 through `writeServiceUnavailable`
  with the fixed body (`sessionUnavailableMessage`). The error text is not in the
  body and is logged with method and path.
- A `GetUserTenantRole` error answers 503 the same way (was 500).
- A non-member (empty role, no error) is still a 403, and the tenant is not
  read for them.
- Table-driven tests fail on main without the change.

## Approach

In `src/pkg/managementapi/tenant_middleware.go`, classify the tenant read
error with `errors.Is(err, ErrTenantNotFound) || errors.Is(err, sql.ErrNoRows)`
and send the rest to `writeServiceUnavailable`. Do the same for the
membership lookup. 503 never lets a request through, so a misclassified error
cannot grant access.

## Files

- `src/pkg/managementapi/tenant_middleware.go`
- `src/pkg/managementapi/tenant_middleware_test.go` (new)
- `plans/backlog.md` (strike 150)

## Not touched

Audit middleware, platform handlers, `TenantAPIKeyMiddleware`.

## Risks

The membership lookup's status changes 500 to 503. Nothing in the UI or docs
keys on the old text ("failed to check tenant membership"); a 5xx is treated
the same by the client.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./pkg/managementapi/...`,
`make lint-tenant`. The new test run against the old middleware fails the five
database-error cases.
