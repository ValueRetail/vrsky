# #708: start-time URL checks for Brightpearl, Front Systems, SAP and Business Central

Backlog item 173. `ValidateNodeConfigs` already refuses bad URLs for Sitoo,
the API consumer, the HTTP producer and Visma when a connection starts. The
four connectors here check only in their own service, so a bad URL shows up as
an `error` status or a DLQ entry instead of a 400 at start.

## Acceptance criteria

- `ValidateNodeConfigs` refuses, on both sides (consumer and producer):
  - Brightpearl: a `base_url` override that is not https, has userinfo, a
    query or fragment, or a literal private address; without `base_url`, a
    `datacenter` that is not one host label or an `account_code` with other
    than letters, digits, `-` and `_`.
  - Front Systems: a bad `base_url`. On the consumer only when `callback_url`
    and `events` are set (a webhook-only consumer never calls it). On the
    producer always, since every delivery calls it, so an empty one is
    refused too.
  - SAP S/4HANA: `host` that is not one host name with an optional port (when
    it is used), `api_base_url`, and with `auth_type` oauth2 `token_url` or the
    token URL derived from `host`.
  - Business Central: `api_base_url` and `token_url` (or the defaults built
    from `aad_tenant_id`).
- One rule per connector, shared by the connector and the management API:
  new package `src/pkg/connectorurl` (`Brightpearl`, `FrontSystems`, `SAP`,
  `BusinessCentral`). Each connector's existing check function keeps its name
  and signature and calls into it, so the connector call sites do not change.
- `ALLOW_PRIVATE_UPSTREAMS` honoured through `safeurl.AllowPrivateFromEnv()`.
- Errors name the field (`brightpearl.base_url`, `sap_s4hana.token_url`, …)
  and never echo userinfo, a query or a fragment (`safeurl.CheckURL`).
- Table-driven tests in `pkg/managementapi` that fail on main, and tests for
  `pkg/connectorurl`. Connector tests pass unchanged.
- Docs: `docs/SECURITY.md` and the four connector pages.

## Touches

- `src/pkg/connectorurl/` (new)
- `src/pkg/managementapi/nodeconfig.go` (dispatch), `connector_urls.go` (new),
  `nodeconfig_connector_urls_test.go` (new)
- Connector check functions only:
  `src/cmd/brightpearl-{consumer,producer}/service.go` (`checkBaseURL`, the
  regexes and `checkDerivedBaseURL` go),
  `src/cmd/front-systems-{consumer,producer}/service.go` (`checkBaseURL`),
  `src/cmd/sap-s4hana-{consumer,producer}/service.go` (`checkURLs`; `checkHost`
  and its regexes go),
  `src/cmd/business-central-consumer/urls.go`,
  `src/cmd/business-central-producer/service.go` (`checkURLs`).
- `docs/SECURITY.md`, `docs/connectors/{brightpearl,front-systems,sap-s4hana,business-central}.md`
- `plans/backlog.md` (strike 173; add 187, the duplicated SAP/BC URL builders, and 188, the dev stack's `management-api` without `ALLOW_PRIVATE_UPSTREAMS`)

## Approach

- Lift each connector's check into `pkg/connectorurl`, unchanged in what it
  refuses. While moving, the error names the real field (`api_base_url`,
  `token_url`, `host`) instead of `safeurl.CheckBaseURL`'s fixed `base_url`.
  Connector tests match on substrings that still hold.
- The SAP and Business Central checks need the default URL a connector builds
  from `host` or `aad_tenant_id`. The shared package builds those itself
  with the same templates. The host part, which is what matters for SSRF, is
  either checked by the host rule (SAP) or a constant (BC, Entra).
- Front Systems: the producer's extra userinfo pre-check is dropped, since
  `safeurl.CheckURL` no longer quotes userinfo (#706). Empty `base_url` is
  refused by the shared rule; both connectors already refused it.
- Management API reads the fields untrimmed, as the connectors get them, so
  " host" is refused at start just as the connector refuses it.

## Risks

- A connection that starts today with, say, an empty Brightpearl datacenter
  will now get a 400. The connector would never have served it, so this
  surfaces a dead config rather than breaking a working one.
- Merge with #704 (producers' `ServesConnection`/`Deliver`/`getConfig`): the
  connector edits are confined to the check functions and imports.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1` on `pkg/managementapi`,
`pkg/connectorurl` and the eight connector packages, `make lint-tenant`.
The new management API tests fail on main (no check there).
