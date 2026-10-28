# #462: CI never runs make lint-connector

## Open questions

None. Touches `.github/workflows/`, so the PR waits for a human
(`agent:needs-human`).

## Goal

`make lint` runs `lint-connector` (#98: every SDK connector calls
`sdk.Run*` and does not import `pkg/messaging` around the SDK), but the Go
lint job in `build-push.yml` does not. A connector that bypasses the SDK
passes CI. Run it in CI and keep it there.

Also backlog item 79: `TestEveryLintCheckStillGates` matched raw step text,
so a commented-out command still counted as a check.

## Acceptance criteria

- `make lint-connector` is clean on main.
- The Go lint job has a "Lint connectors" step running `make lint-connector`,
  no `continue-on-error`, no step `if`. `go-lint` stays in the `needs:` of
  both `go-tests` and `plan`.
- `TestEveryLintCheckStillGates` lists the connector lint and fails when the
  step is deleted, commented out in YAML, or its command is commented out
  inside the script.
- The guard matches each step's commands with shell comments removed
  (`stripShellComments` from `gotestshards_guard_test.go`, same package).
- Backlog items 67 and 79 struck with (#462).

## Files

- `.github/workflows/build-push.yml` (one step in `go-lint`)
- `src/pkg/managementapi/lintgates_guard_test.go`
- `plans/backlog.md`

## Timing

`lint-connector` imports only the standard library. Locally: 1.9 s with an
empty build cache, 0.26 s warm. The job restores its build cache (#463), so
expect well under a second in CI, against about 1 m 30 s for the job.

## Verify

- Mutations on the workflow: delete the step, comment it out, comment out the
  command inside a block script, trailing YAML comment, `continue-on-error`,
  step `if: false`; plus commented `make lint-tenant` and `gofmt -l .` for
  item 79. Each must fail the guard. Without the comment stripping the three
  in-script comment cases must pass (shows item 79 was real).
- `make lint-connector` with `LINT_CONNECTOR_ROOT` pointing at a bad
  connector exits non-zero.
- From `src/`: build, vet, golangci-lint, `go test -count=1
  ./pkg/managementapi/`, `make lint-tenant`.
- On the PR: the Go lint job runs "Lint connectors" and is green.
