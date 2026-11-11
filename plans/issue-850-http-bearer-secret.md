# #850: HTTP producer sends a bearer token from a stored secret

Issue: ValueRetail/vrsky#850. Needed by the Bifrost sales → Retailhero
template: Retailhero authenticates VRSky as a tenant workload with a bearer
credential (Retailhero ADR-0063, decision 3).

## First check: does a `{secret:<id>}` placeholder already work?

Yes. `TestBearer_PlaceholderInAuthorizationHeaderIsExpanded` stores a secret
through the real secrets table and sets
`http.headers.Authorization = "Bearer {secret:<id>}"`. The target receives the
token, because the producer resolves its whole node config with
`crypto.ResolveSecretsInJSON`.

It is still not the way to do it:

- the editor has no field for it, so a user types the header by hand, and a
  plaintext token in that header looks the same to the producer;
- an expanded value can come back in an error text (#853, tracked there);
- nothing can refuse a plaintext token in a free-form header.

So the issue's own design is built, and the placeholder is documented as
possible but not recommended.

## Acceptance criteria

- `http.auth_type: "bearer"` with `http.token_secret_id` sends
  `Authorization: Bearer <token>`. The secret is read for the message's
  tenant only.
- A plaintext `http.token` is refused, at connection start
  (`validateHTTPProducerBearer`) and by the producer (nothing sent, retried,
  then the DLQ). So is `auth_type: bearer` without a stored token. Errors
  name the setting, not the value.
- The token is never in a log line, the activity feed or the error that
  becomes `X-Vrsky-Last-Error`. A target that echoes the request headers
  gets the token masked in the activity feed's response preview.
- 401/403 follow #838: retried, then the DLQ.
- `docs/connectors/http.md`, the pipeline editor (a "Bearer token"
  SecretInput under Authentication), the Bifrost sales template (a secret
  wizard field, `auth_type: bearer`) and its doc's "Setting it up".

## Files

- `src/cmd/http-producer/main.go`; tests in `bearer_secret_test.go`
  (Postgres-backed) and `bearer_leak_test.go` (sqlmock, harness)
- `src/pkg/managementapi/nodeconfig.go`, `nodeconfig_api_test.go`
- `ui/src/components/Pipeline/PropertyEditor.tsx`, `ui/src/types/nodeConfig.ts`,
  `ui/src/onboarding/templates.ts`, `ui/src/onboarding/templates.test.ts`
- `docs/connectors/http.md`, `docs/connectors/bifrost-sales-to-retailhero.md`

## Decisions

- The setting sits flat in the `http` block (`auth_type`, `token_secret_id`)
  next to the existing `auth_type`/`oauth_grant_id`, not in a nested `auth`
  object. The UI's secret minting turns `token` into `token_secret_id`
  without new code.
- The bearer replaces an `Authorization` header from `http.headers`, as the
  OAuth token does.
- The response preview masks every token the request carried (bearer and
  OAuth), including one cut by the 4 KiB limit. It was not asked for, but an
  echoing target would otherwise put the token in the feed every tenant user
  can read.
- The doc's open questions are left to the #851/#852 PR, which rewrites that
  list, to keep the two PRs apart.

## Verification

Go: gofmt, build, vet, golangci-lint, lint-openapi, lint-tenant,
lint-connector, `go test -race -count=1 ./...` against a local Postgres 16.
UI: tsc, lint, build, coverage. Mutation runs: inline-token refusal off,
preview masking off, start check off; each made its tests fail.
