# Issue 575: TestRun_ProductionPath flakes in full runs

## Cause

`TestRun_ProductionPath` (`src/pkg/sdk/runner_test.go`) had two problems.

1. It waited for the health server, then probed the auxiliary HTTP server
   once. `startAuxHTTP` listens in its own goroutine, so under load the probe
   could run before the listener existed.
2. It used fixed ports 18195 and 18196. With `-count=N`, or another package
   using the same ports, a listener from the previous run could still be
   bound. The new run then logged "address already in use" and the test
   talked to the old server. That shows up as `/readyz` 503 with "nats not
   connected". I reproduced this under CPU load with `-count=100`.

## Fix (test only)

- Wait for the auxiliary server with `harness.Eventually`, the same way the
  test waits for the health server. The 204 assertion is unchanged.
- Pick free ports at runtime (`freePort`) instead of fixed ones.

The SDK reads `HEALTH_PORT` and `WORKER_HTTP_PORT` as port numbers and
listens on `":"+port`, so it cannot listen on `:0` and report the port back.
Changing that would alter runtime behaviour shared by every connector, so
the test picks the port. A port could be taken between the pick and the
listen; that window is tiny and far smaller than fixed ports.

## Touches

`src/pkg/sdk/runner_test.go` only.

## Acceptance

- `go test -race -count=50 -run TestRun_ProductionPath ./pkg/sdk/` passes,
  also with six busy loops running.
- `go test -race ./...` passes three times in a row.
- No assertion removed or weakened.
