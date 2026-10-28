# Issue #410: affected.sh and test-only or src/internal changes

Backlog items 38 and 54.

## Open questions

None. Two calls made without asking, recorded here and in the PR:

- The test-only skip applies to every path under `src/`, `src/internal`
  included, not only the `src/pkg` and `src/cmd` cases the issue names. A
  `_test.go` file never ends up in a binary wherever it lives.
- `testdata/` is skipped at any depth under `src/`. No non-test Go file
  reads or embeds a `testdata/` directory today (the only `go:embed` lines
  are the two `.ps1` scripts in `src/pkg/managementapi/agent_release.go`).

## Goal

The deploy job rebuilds and rolls exactly the images a merge can change:

- A change that only touches `_test.go` files or `testdata/` under `src/`
  marks no image. Today it rebuilds about 37 images and restarts every
  connector whose digest moved.
- A change under `src/internal/` marks every Go image, like `src/pkg/`.
  Today it marks none, though seven Dockerfiles copy `src/internal/` and the
  rest copy all of `src/` (`vrsky-cli` among the seven).

## Files

- `infrastructure/azure/affected.sh`: two `case` changes and the header.
- `src/pkg/managementapi/nodeconfig_test.go`: new `TestAffectedServices`
  cases (written by a separate test author from the acceptance criteria).
- `plans/backlog.md`: strike items 38 and 54 with `(#410)`.

## Approach

In the `case` in `affected.sh`, before any `src/` branch:

```bash
src/*_test.go|src/*/testdata/*)        ;;   # never in an image
```

Bash `case` globs match `/` with `*`, so this covers every depth. Then add
`src/internal/*` to the `src/pkg/*|src/go.mod|src/go.sum` branch.
`monitoring`, `backup`, `connectors-manifest` and the UI are untouched.

A mixed change (a test file plus a non-test file in the same package)
still marks every image through the non-test path.

## Risks

- A future non-test file that embeds `testdata/` would be missed. Unlikely
  (Go convention keeps `testdata` for tests) and the cost is one missed
  rebuild that the next shared change catches.
- The script is under `infrastructure/azure/**`: PR plus
  `agent:needs-human`, no agent merge.

## Verification

- `TestAffectedServices` cases: test-only (`src/pkg`, `src/cmd/<x>`),
  testdata-only, mixed, `src/internal`.
- Mutations: drop the skip line, drop `src/internal/*`, narrow the skip to
  `_test.go` only; each must fail a test. Script restored after.
- `go test -count=1 ./pkg/managementapi/...`, `go vet ./...`,
  `golangci-lint run`, `bash -n infrastructure/azure/affected.sh`.

## Review follow-ups (same PR)

- `build-push.yml`'s plan step runs `git diff --name-only --no-renames`.
  With rename detection on, `git mv x.go x_test.go` printed only the new
  path, which the test-only skip now drops, so nothing deployed.
  `TestDeployJobGatesOnTests` asserts the flag.
- `infrastructure/migrations/*` and `src/cmd/vrsky-agent/*` map to
  `image management-api` and `core management-api`. Its Dockerfile bakes
  both in; before this a migrations-only merge shipped nothing. These were
  the only paths outside `src/pkg`, `src/internal` and an image's own
  `src/cmd/<x>` that any Dockerfile copies.
- Mutations: drop each mapping, map migrations to the image without the
  rollout, drop `--no-renames`. All fail a test.
