# #768: db-producer holds the pool lock while connecting

## Goal
`TargetConnection.pool` in `src/cmd/db-producer/main.go` held `tc.mu` through
open + ping (up to the 10 s connect timeout). `close` (cache eviction on the NATS
command goroutine, and `Stop`) takes the same lock, so a start/stop command or a
shutdown could wait up to 10 s.

## Approach
- Connect outside `tc.mu`. Take the lock to check state, release, connect,
  retake it to publish the pool.
- If the target was closed meanwhile, close the new pool and return `errTargetEvicted`.
- Concurrent first uses: one caller connects (`tc.connecting`), others wait on its
  result (or their own ctx). One pool per target, no loser to leak.
- A failed open or ping returns the error and is not cached; the next call retries.

## Acceptance criteria
- Eviction and Stop during a slow connect return promptly.
- Opens equal closes after eviction/Stop mid-connect; concurrent first uses open once.
- Failed open/ping stays retryable and uncached; #751 delivery tests unchanged.

## Files
`src/cmd/db-producer/main.go`, `src/cmd/db-producer/evict_test.go`, `plans/backlog.md` (if listed).

## Risks
A waiter inherits the connecting caller's error (including its ctx cancel). That
is a retryable error on a NATS redelivery, same class as before.

## Verification
`go build/vet`, `golangci-lint`, `go test -race -count=1 ./cmd/db-producer/...`,
`make lint-tenant lint-connector`; mutation: hold `tc.mu` across the connect and
see the new tests fail.
