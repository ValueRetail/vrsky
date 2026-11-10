# HTTP producer: timeout for streamed uploads (#774)

## Goal
A large offloaded payload streamed as the request body must not be cut off at
30 s while it is making progress. A target that never answers, an upload that
stalls, or one that crawls still fails within a bound, on both the buffered
and the streaming path.

## Acceptance criteria
- Slow upload that keeps moving (longer than the timeout) succeeds.
- Stalled upload fails within about one timeout, as a retriable deadline error.
- Target that takes the body and never answers fails within a bound.
- Target that sends headers and part of a body and then stalls is cut off.
- Buffered path unchanged: 30 s end to end, OAuth token fetch not counted.
- A streamed upload has an overall per-attempt limit that progress does not
  reset (review round 1, finding 1).
- `docs/connectors/http.md` (Failed deliveries) says what really bounds a
  request.

## Touched
`src/cmd/http-producer` (main.go, tests), `docs/connectors/http.md`,
`plans/backlog.md` (item 193 struck). Not `pkg/messaging` or `pkg/safeurl`.

## Approach and decisions
- `http.Client.Timeout` counts body writing, so the clients now have none
  (`safeurl.NewClientWith(0, ...)`; safeurl untouched, other callers keep theirs).
- Each attempt gets a `reqGuard`: a timer that cancels a derived context. A
  buffered request never resets it (same 30 s end to end as before). A streamed
  one resets it whenever the transport takes body bytes from the source, so the
  limit becomes "30 s without progress". Chosen over `ResponseHeaderTimeout` +
  a separate watchdog: one mechanism covers connect, stalled write, stalled
  source, wait for headers and the response body read.
- The guard stays armed until the response body has been read (stopped by the
  deferred `stops`), so headers followed by a stalled body are cut off too. The
  status decides the outcome: a 200 whose body stalls is still a delivery, as
  it was with the old client timeout.
- Overall limit for a streamed attempt (orchestrator decision after review):
  request timeout + payload size / 256 KiB/s, at most 15 minutes; 15 minutes
  when the size is not known. Constants `minStreamRate` and `maxStreamTime`
  next to `defaultRequestTimeout` in `main.go`, with the reasoning in the
  comment; `streamMinRate`/`streamMaxTime` fields let tests inject small ones.
  Implemented as a second timer in the same guard, so one cancel path and one
  error wrap. Needed because the delivery context is `context.Background()`
  and dispatch is serial: without it a target reading 2-4 KB/s resets the
  progress timer for ever and blocks every tenant.
- "Progress" is bytes leaving the source into the transport, not bytes the
  target has read. Once the socket send buffer (a few MB) is full, the
  transport stops taking bytes, so a target that stops reading still stalls the
  timer. At EOF up to a buffer's worth may still be in flight, so the guard is
  set to twice the timeout there (60 s by default): one for the drain, one for
  the answer. Scaling with the timeout keeps the tests fast. Reads after EOF do
  not shrink it again.
- The OAuth token is resolved before the guard starts, so a slow token service
  does not use up the 30 s for the request (review finding 5). The
  `Authorization` header is still set after the node's custom headers, so it
  still wins over a custom `Authorization`.
- Timeout errors keep the redacted transport error and say what ran out:
  `<err>: timed out after 30s` (buffered), `<err>: timed out after 30s without
  progress` (streamed), `<err>: upload not finished within its <limit> limit`
  (overall). All wrap `context.DeadlineExceeded` so they stay retriable.
- The transport waits for its body write before reporting a cancel, so a source
  blocked in Read would hang past the timeout. `progressReader` runs the source
  read in a goroutine on a private buffer and returns on context end. Cost: one
  goroutine and a copy per chunk; the abandoned read ends with the source.
- Spec change: `TestSend_DefaultTimeoutIs30s` pinned `Client.Timeout == 30s`.
  Replaced by a test asserting no client timeout and a 30 s default guard; the
  behavioural no-answer tests (#763) are unchanged and still pass.

## Risks
- Extra goroutine per streamed chunk; small next to network I/O.
- An abandoned read could outlive a delivery until the source is closed.
  Untested assumption: closing the object stream (`rc.Close()` in the SDK)
  unblocks a Read that is in progress on S3/Azure/GCS bodies. Those bodies are
  HTTP response bodies, where Close does interrupt a Read, so the risk is low,
  but no test covers it here.
- The overall limit is a product trade-off: above about 218 MiB an upload needs
  better than 256 KiB/s on average (1 GiB about 1.2 MiB/s, 10 GiB about
  11.8 MiB/s), or every attempt is cut off and the message is dead-lettered.
  Fine for targets on a datacenter link; a slow target with multi-GB payloads
  would need the constants raised, or a per-node setting later.

## Verification
Tests (200 ms timeout unless noted): slow-but-moving upload succeeds; stalled
upload fails retriably; no answer after upload fails; delivery context bounds a
moving upload; slow upload is cut off at its overall limit (size-based and
capped rows); `streamLimit` table; headers then stalled body, buffered and
streamed; slow token fetch does not time out the request; answer within twice
the timeout after EOF counts; wrap messages keep the cause.

Mutations, each made to fail a test: no overall limit; limit ignores size;
progress also resets the overall limit; guard stopped right after `client.Do`;
guard started before the token fetch; single timeout after EOF; (round 1)
guard never reset.

go build/vet, golangci-lint, `go test -race`, lint-tenant, lint-connector.
