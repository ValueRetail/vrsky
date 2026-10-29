# Fix the flaky TestDonePersistPG_RestartAndTenants (#570)

## Cause

`pdPG.start` calls `startConnection` and then reads the connection status
and expects `running`. `startConnection` writes `running` and launches the
poller. With `poll_interval_seconds: 0` the poller is a one-shot: with
nothing new it finishes within milliseconds and writes `stopped`. The read
races that write. The product order is right (`running` is always written
first); only the test's read is unsynchronised. Measured on main with
`-race -count=40`: 4 failed runs of 40 in sftp-consumer, 4 of 40 in
cloud-storage-consumer, all "status after the start = "stopped", want
running".

## Fix

Test files only, same change in both packages
(`src/cmd/sftp-consumer/done_persist_pg_test.go`,
`src/cmd/cloud-storage-consumer/done_persist_pg_test.go`).

The poller opens its source (`dial` / `newStore`) before it does anything
else. `pdPG.start` now makes that call wait on a gate. The test starts the
connection, reads the status (still `running`, since the poller cannot
finish), then opens the gate and waits for `stopped` as before. A cleanup
opens the gate so a failed assertion cannot leave the poller blocked.

## Acceptance criteria

- No read of the status can race the one-shot's `stopped` write.
- `go test -race -count=40 -run TestDonePersistPG_RestartAndTenants` passes
  in both packages with `VRSKY_TEST_POSTGRES_URL` set.
- Nothing is weakened: the `running` check stays, and the restart, done-set
  and tenant assertions are untouched.
- `go vet`, `golangci-lint` and `go test -race -count=3` pass in both
  packages.

## Touches

`src/cmd/sftp-consumer` and `src/cmd/cloud-storage-consumer`, test files
only.
