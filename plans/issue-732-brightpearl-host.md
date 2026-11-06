# #732 Brightpearl default host

Goal: the default API host follows Brightpearl's own docs.

- Source: https://api-docs.brightpearl.com/integration/account-configuration/get.html
  and https://help.brightpearl.com/hc/en-us/articles/212645003-URI-syntax:
  `https://{datacenter}.brightpearlconnect.com/public-api/{account}`, `euw1`, `use1`.
- Legacy `ws-{code}.brightpearl.com` hosts still resolve; old codes map
  `eu1` to `euw1`, `use` to `use1`.
- US West (`usw`, `usw1`) has no documented new name, so it keeps
  `ws-usw.brightpearl.com`.
- One builder, `connectorurl.BrightpearlBaseURL`, used by both connectors.
  `base_url` still overrides.

Touches: `src/pkg/connectorurl`, `src/cmd/brightpearl-{consumer,producer}`,
`ui/.../PropertyEditor.tsx` placeholder, `docs/connectors/brightpearl.md`.

Verify: unit tests for each code, connector and management API tests,
`make lint-connector`, `make lint-tenant`. A human should confirm against a
real account.
