# Source type `direct`: a pipeline fed by an outside publisher

## Goal

Bifrost's Basket service publishes finished VRSky envelopes straight into
`VRSKY_DATA` on `vrsky.data.<tenant>.pipeline.<connection id>` (Bifrost ADR
0002): base64 payload, `tenant_id` = the VRSky workspace id, `integration_id`
= the connection id. data-filter, data-converter and http-producer already
take such a message: they read the connection from `env.IntegrationID` and
run the nodes fed by the consumer when `_last_processed_by` is empty. A live
run proved it.

What is missing is a source type for it. A connection needs a consumer node,
so the live run used an idle `http` webhook source as a stand-in, and the
`bifrost-sales-to-retailhero` template starts with a `nats` source reading
bare Basket events, which is not what Basket does (the NATS source would wrap
Basket's envelope in a second one).

## Acceptance criteria

- A consumer node `{"type": "direct"}` is accepted on save and on start, with
  no settings. As a destination it is refused.
- Starting a connection with a `direct` source launches nothing. Start already
  only sweeps legacy workers and publishes a fire-and-forget start command;
  nothing waits for a consumer to claim the node, so the connection is marked
  running at once. A test holds that.
- The builder offers "Published into VRSky" as a source type. Its panel shows
  the stream and the subject the publisher must use, with the workspace id and
  the connection id once the pipeline is deployed.
- The deploy summary in the builder shows that subject.
- `bifrost-sales-to-retailhero` starts with a `direct` source; the NATS fields
  are gone. After deploy the wizard shows the connection id, the workspace id
  and the subject, and says to set `BASKET_VRSKY_INTEGRATION_ID` to the
  connection id and Basket's tenant to the workspace id.
- Docs: a connector page for the type, the overview table and nav, and open
  question 2 of `docs/connectors/bifrost-sales-to-retailhero.md` resolved.

## Files

- `src/pkg/managementapi/nodeconfig.go` (+ tests in `nodeconfig_test.go`,
  `handler_test.go`)
- `src/cmd/data-filter/bifrost_sales_test.go`: a Basket envelope, as Basket
  publishes it, through the template's filter read from the database rows of a
  connection with a `direct` source
- `ui/src/components/Pipeline/DirectSourceInfo.tsx` (new) + test,
  `PropertyEditor.tsx`, `pages/PipelineBuilder.tsx`, `types/nodeConfig.ts`
- `ui/src/onboarding/templates.ts`, `OnboardingWizard.tsx` and their tests
- `docs/connectors/direct.md` (new), `index.md`,
  `bifrost-sales-to-retailhero.md`, `docs/guide/connectors-and-nodes.md`,
  `docs/llms.txt`, `docs/llms-full.txt`, `mkdocs.yml`

## Decisions

- The rule has no service (`service: ""`). No standing connector claims the
  node: the pipeline workers serve the nodes after it. The deploy drift guard
  already skips a rule without a service.
- The golden filter and converter configs in `ui/src/onboarding/testdata/` do
  not change: the payload the filter sees is the Basket event either way. The
  Go tests that read them still pass unchanged; the new filter test reads the
  filter golden through the `direct` connection.
- The wizard's `asList` field option existed only for this template's NATS
  server field. With that field gone no template uses it, so it is removed
  rather than left untested.
- No sample-data preview or field discovery for this source: nothing writes
  `last_payload` for it (the SDK consumer does that, and there is none), and
  the sample-data endpoint falls back to the newest payload of another
  pipeline in the workspace. The builder says so instead of calling it.
- The deploy drift guard skipped any rule without a service; it now fails on
  one unless it is in `serviceless` (only `consumer/direct`).

## Found on the way (backlog, not fixed here)

- The workers read the connection by `integration_id` alone; only secrets
  are resolved under the envelope's tenant (backlog 231, tenant isolation).
- Stop does not stop a `direct` pipeline: the workers ignore the
  connection's status, and there is no VRSky consumer to stop (backlog 232).
  Documented on `docs/connectors/direct.md`.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test -race` on `pkg/managementapi`, `cmd/data-filter`,
`cmd/data-converter`, `cmd/http-producer`, `make lint-tenant`.
From `ui/`: `npm run lint`, `npm run build`, `npx vitest run --coverage`.
`mkdocs build --strict` if mkdocs is installed.
