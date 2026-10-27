# Root Makefile forwards targets that don't exist (#361)

No open questions.

## Goal

The root `Makefile` forwards eight targets to `src/Makefile` that it no
longer defines, so running them ends in "No rule to make target". Root
`make help` also leaves out the three targets the root owns. Make every
target root `make help` shows real, and keep it that way with a guard test.

## Acceptance criteria (from the issue)

- Every target root `make help` lists exists and works (or does a clear dry
  run). The eight dead forwards (`docker-build`, `docker-push`, `run`,
  `build-consumer`, `docker-build-consumer`, `docker-push-consumer`,
  `run-consumer`, `e2e-test`) are removed, or pointed at their successor if
  git history shows one.
- Root `make help` lists `up-core`, `up-core-remote` and `down-core` with one
  line each.
- A guard test fails when the root Makefile forwards a target `src/Makefile`
  doesn't define, and when root `make help` lists a target that doesn't
  exist.
- `AGENTS.md` stays accurate about the root targets.
- `plans/backlog.md` item 12 struck with `(#361)`.

## History

All eight came out of `src/Makefile` in f9295a53 (#205), which retired the
per-connection `cmd/consumer` and `cmd/producer` workers. Their replacement is
the standing connector fleet in compose and Kubernetes, not a make target, so
there is nothing to point them at. They get removed.

## Files

- `Makefile` (root)
- `src/pkg/managementapi/makefile_guard_test.go` (new; not
  `nodeconfig_test.go`, which open PRs #359 and #364 also change)
- `AGENTS.md` (the paragraph on root targets)
- `plans/backlog.md` (strike item 12)

## Approach

- Drop the eight dead forwards and their `.PHONY` entries. Add `info` to
  `.PHONY` (it was forwarded but not declared).
- Root `make help` runs `src`'s help, which lists every `##` target in
  `src/Makefile`. Seven of those (`build-agent`, `test-db`, `lint-tenant`,
  `lint-connector`, `lint-openapi`, `docker-build-ui`, `docker-push-ui`) have
  no root forward, so root help lists targets that fail from the root. Add
  plain forwards for them, same shape as the others.
- Give `up-core`, `up-core-remote` and `down-core` a `## description` and have
  root `help` print them after `src`'s list, with the same grep/awk format.
- Guard test, written by a separate test-author subagent from the acceptance
  criteria: parses both Makefiles (and/or runs `make help`/`make -n`) and
  fails on a forward to a missing `src` target or a help entry with no rule.

## Risks

- `make help` output format changes slightly (an extra section). Nothing
  parses it except the new test.
- The guard test reads repo files, so it needs `-count=1` to see edits.
- If `make` isn't on the CI runner the test should skip rather than fail; a
  static parse avoids that.

## Verification

- From the root: `make help`, then `make -n <target>` for every target help
  lists; the eight dead names give "No rule to make target".
- From `src/`: `go vet ./...`, `golangci-lint run ./pkg/managementapi/...`,
  `go test -count=1 ./pkg/managementapi/ -run Makefile`.
- Mutation: re-add a dead forward, and drop the `##` from an up-core line;
  the guard fails each time.
