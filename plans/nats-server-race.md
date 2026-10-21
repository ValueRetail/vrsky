# The nats-server data race that flakes Go Tests

Seen 2026-10-21: PR run 39565363105 failed
`TestConnectionScoped_ForeignRefEnvelopeAckedWithoutRehydrate` with "race
detected during execution of test". Both stacks are inside the embedded
`github.com/nats-io/nats-server/v2 v2.11.15`; nothing of VRSky's is on
them except the harness goroutine that starts the server.

## What the race is (read from the module source)

- **Write**: `(*stream).updateWithAdvisory` sets `mset.cfg = *cfg`
  (`stream.go:2233`) while handling one `UpdateStream` request.
- **Read**: `(*jsAccount).tieredReservation` (`jetstream_api.go:1529`),
  handling a *second* `UpdateStream` request, iterates every stream of
  the account and reads `sa.cfg.Name/MaxBytes/Storage/Replicas` of the
  others **without the stream's lock**.
- **Trigger on our side**: `pkg/messaging.ensure` calls
  `js.UpdateStream` for every stream that already exists, and in the SDK
  tests the runner, the consumer and the producer each call
  `EnsureStreams` at start, so `VRSKY_DATA` and `VRSKY_DLQ` get concurrent
  updates. Real deployments do the same whenever several connectors start
  at once; without `-race` it is a benign torn read of a config struct,
  not a crash.
- **Fixed upstream in v2.15.0**: `tieredReservation` now takes
  `mset.cfgMu.RLock()` around the read. `v2.11.16`, `v2.12.0` and
  `v2.14.0` still read unlocked (checked in the module cache).
  `v2.15.0` requires `nats.go v1.51.0`; `v2.15.1` requires `v1.53.1`.
  We are on `nats.go v1.49.0`, imported by 49 non-test files.

## Open questions

1. **Which versions.** Recommended: **nats-server v2.15.1 and nats.go
   v1.53.1**, the pair the newest server pins. The server is imported by
   the test harness only (`pkg/sdk/harness/nats.go`); the client bump is
   the one that touches production, four minor versions of a library
   whose API has been stable through the 1.x line. Alternative: v2.15.0
   with nats.go v1.51.0, the smallest client bump that carries the fix.
2. **Also stop updating unchanged streams?** `ensure` could compare the
   existing config with the wanted one and skip `UpdateStream` when
   nothing differs, which removes the trigger as well as some log noise
   on every connector start. Recommended: **not in this PR**; it is a
   behaviour change in `pkg/messaging` with its own tests, and the
   upstream fix is the real one. Proposed as a follow-up if wanted.

## Goal

The race cannot be reported again from this code path, `-race` stays on
in CI, and no test is weakened or retried.

## Approach

```bash
cd src && go get github.com/nats-io/nats-server/v2@v2.15.1 github.com/nats-io/nats.go@v1.53.1 && go mod tidy
```

Then, in this order:

1. Reproduce before the bump: `go test -race -count=30 ./pkg/sdk/ -run
   TestConnectionScoped` on v2.11.15 (the race is timing-dependent; a
   reproduction is a bonus, not a gate).
2. After the bump: the same command, then `go test -race ./pkg/sdk/...
   ./pkg/messaging/... ./cmd/...`, then the whole suite as CI runs it,
   `go test -race -cover ./...`.
3. `govulncheck ./...` with the allow-list check from `vuln-scan.yml`
   (a newer nats-server can only close advisories, not open them).
4. `gofmt`, `go vet`, `golangci-lint run`.

## Files

| File | Change |
|---|---|
| `src/go.mod`, `src/go.sum` | the two bumps and whatever `tidy` pulls with them |

No code change expected; if `nats.go v1.53.1` breaks a call site the
compiler says where, and that goes in the same PR with a note.

## Verify

The PR's *Go Tests*, *Integration Tests* and the nightly end-to-end are
green; the race report cannot recur because the read is now locked.
Merge rebuilds every Go image (`go.mod` is an all-go change) and rolls
the fleet.

## Risks

- **nats.go 1.49 → 1.53 in production**: client-side behaviour changes
  in reconnect, JetStream ordered consumers or headers would show in the
  SDK and messaging suites, which assert what arrives, and in the nightly
  end-to-end. The rollout is the usual digest-by-digest one with the
  smoke check.

## Non-goals

Skipping `UpdateStream` for unchanged streams (question 2); pinning
`-race` off for any package; retrying flaky tests in CI.
