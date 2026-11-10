# Issue #717: errors shown to tenants leak internal addresses or credentials

Backlog items 160, 172, 176.

## Open questions

None blocking. Decisions below.

## Goal

A tenant reads a failed request's error in the activity feed, a sample-data
response, or a dead-lettered message's `X-Vrsky-Last-Error`. None of those may
carry a resolved address, a resolver address or a credential. Detail stays in
the server log.

## Acceptance criteria

- A refused dial's `Error()` has no address (also not the `dial tcp [addr]:port`
  prefix `net` adds); the address is available through `safeurl.Detail` and
  logged by the dial check.
- `safeurl.UserMessage(err)` maps dial refusal, DNS failure, TLS verification,
  timeout, cancellation, connection refused/failed, refused redirects and any
  other `*url.Error` to fixed texts; it returns false for non-transport errors.
- The sample-data handlers of Sitoo, API, Visma, Brightpearl, SAP S/4HANA and
  Business Central consumers show those texts and log `safeurl.Detail(err)`.
- `RedactURL` shows `[invalid URL]` for an "@" in the query when no path
  precedes the "?" (item 176); `CheckRequestURL` and the HTTP producer inherit it.

## Touches

`src/pkg/safeurl`, `src/cmd/{api,sitoo,visma,brightpearl,sap-s4hana,business-central}-consumer/server.go`,
`docs/SECURITY.md`, `plans/backlog.md`.

## Decisions taken without asking

- Fix the dial refusal at the source (`blockedError`, Error() without address)
  instead of mapping per connector: it covers every producer's DLQ header at
  once. `errors.Is(err, ErrBlockedAddress)` still works.
- `NewClientWith` unwraps the `*net.OpError` around a refusal; otherwise the
  address stays in the text.
- Item 176: "@" in the query with no path and no userinfo is `[invalid URL]`.
  A query with a path (`/x?email=a@b`, pinned by the HTTP producer test) is
  unchanged. A password containing "?" cannot parse with a path before the "?".
- Non-transport errors (HTTP status text, parse errors) keep their text.
- Redirect refusals keep their text via `redirectError` (hosts and redacted URLs only).

## Out of scope (other workers / follow-up)

Producers and the cloud-storage consumer, `pkg/managementapi`, `pkg/sdk` and
`pkg/messaging` are not touched. A producer that returns the raw `client.Do`
error still puts `Get "https://host/path?query": dial tcp ...: connect: connection
refused` into `X-Vrsky-Last-Error`: the URL (with its query) and a resolved
address of an unreachable but allowed host. Follow-up: map with
`safeurl.UserMessage` where the producers (or `pkg/sdk`) build the DLQ header.

## Verification

Go build, vet, lint, `go test -race` on `pkg/safeurl`, the touched consumers and
all producers; mutation run on the DNS mapping and on the OpError unwrap.
