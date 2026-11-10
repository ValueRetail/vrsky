# HTTP producer: timeout for streamed uploads (#774)

## Goal
A large offloaded payload streamed as the request body must not be cut off at
30 s while it is making progress. A target that never answers, or an upload that
stalls, still fails within a bound, on both the buffered and the streaming path.

## Acceptance criteria
- Slow upload that keeps moving (longer than the timeout) succeeds.
- Stalled upload fails within about one timeout, as a retriable deadline error.
- Target that takes the body and never answers fails within the timeout.
- Buffered path unchanged: 30 s end to end.
- Delivery context still bounds the whole request.
- `docs/connectors/http.md` (Failed deliveries) updated.

## Touched
`src/cmd/http-producer` (main.go, tests), `docs/connectors/http.md`,
`plans/backlog.md` (item 193 struck).

## Approach and decisions
- `http.Client.Timeout` counts body writing, so the clients now have none
  (`safeurl.NewClientWith(0, ...)`; safeurl untouched, other callers keep theirs).
- Each attempt gets a `reqGuard`: a timer that cancels a derived context. A
  buffered request never resets it (same 30 s end to end as before). A streamed
  one resets it whenever the transport pulls body bytes and at EOF, so the
  limit becomes "30 s without progress", then 30 s for the answer. Chosen over
  `ResponseHeaderTimeout` + a separate watchdog: one mechanism covers connect,
  stalled write, stalled source, wait for headers and the response body read.
- The transport waits for its body write before reporting a cancel, so a source
  blocked in Read would hang past the timeout. `progressReader` runs the source
  read in a goroutine on a private buffer and returns on context end. Cost: one
  goroutine and a copy per chunk; the abandoned read ends with the source.
- Guard timeouts are returned wrapping `context.DeadlineExceeded` so the error
  stays retriable and matches the existing assertion.
- Spec change: `TestSend_DefaultTimeoutIs30s` pinned `Client.Timeout == 30s`.
  Replaced by a test asserting no client timeout and a 30 s default guard; the
  behavioural no-answer tests (#763) are unchanged and still pass.

## Risks
Extra goroutine per streamed chunk (32 KiB-ish); acceptable next to network I/O.
An abandoned read could outlive a delivery until the source is closed.

## Verification
New tests: slow-but-moving upload, stalled upload, no answer after upload,
delivery context bound. Mutation: disabling the guard reset makes the slow
upload test fail. go build/vet, golangci-lint, `go test -race`, lint-tenant,
lint-connector.
