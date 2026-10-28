# #437: rebuild only the images that import a changed package

## Goal

A change to a `.go` file under `src/pkg` or `src/internal` rebuilds the
images whose `main` package depends on that package, not all 36 Go images.
When the mapping can't vouch for a change, rebuild everything.

## Acceptance criteria

- `infrastructure/azure/affected.sh` maps a changed non-test `.go` file under
  `src/pkg/` or `src/internal/` to its package, and marks the images whose
  binary links it (`go list`, `.Deps`).
- Rebuild every Go image for: `src/go.mod`, `src/go.sum`, any
  `src/cmd/*/Dockerfile`, a non-`.go` file under `src/pkg` or `src/internal`
  (`go:embed` assets, assembly, cgo sources), a `.go` file whose directory is
  no package `go list` knows, and any failure of `go list`.
- Unchanged: `_test.go` and `testdata/` skip; vrsky-agent and migrations go to
  management-api; monitoring, backup and connectors-manifest rules.
- The plan job has Go and the module cache and runs with `GOPROXY=off`.
- `TestAffectedServices` covers: a package one connector imports, the widest
  package, `go.mod`, an embedded non-`.go` file, an unresolvable path, helper
  failure, a test-only file.

## Approach

Option (a): `affected.sh` calls `go list` itself, only when a shared `.go`
file changed and nothing else already forces a full rebuild.

- `go list -m` for the module path.
- `GOOS=linux` and `GOOS=windows` `go list ./...` for the set of known
  packages. A changed directory outside that set (deleted package, or one
  that builds for neither) fails the mapping.
- `GOOS=linux go list -f '{{.ImportPath}}{{range .Deps}} {{.}}{{end}}'` over
  every image's `./cmd/<name>`; `.Deps` is the transitive closure.
- `GOOS=windows` the same for `./cmd/vrsky-agent`, counted toward
  management-api, whose Dockerfile builds and ships that binary.
- All with `CGO_ENABLED=0 GOARCH=amd64`, as the Dockerfiles build.
- `AFFECTED_GO` overrides the go binary, so tests can make the helper fail.

Why not (b), a checked-in import map with a guard test: it would be a second
copy of the import graph that every new import has to update, and the guard
test would only catch drift after the fact. `go list` on the runner is the
graph the build uses, and costs a few seconds.

Network: the plan job sets up Go with the module cache keyed on `go.sum`,
runs the shared retried `go mod download` action, then runs `affected.sh`
with `GOPROXY=off`. If a module is missing, `go list` fails and the script
rebuilds everything.

Found on the way: `has()` matched a name only with spaces on both sides, and
`CONNECTORS` spans lines, so 14 connectors at a line edge never matched. A
change to their own `src/cmd/<name>/` code deployed nothing. Fixed here
(newlines count as spaces), since `mark` relies on it; the test author's
"own code" cases cover every image.

## Files

- `infrastructure/azure/affected.sh`
- `.github/workflows/build-push.yml` (plan job: setup-go, module download,
  `GOPROXY=off`)
- `src/pkg/managementapi/nodeconfig_test.go`

## Risks

- A build tag or file the Dockerfiles use that `go list` doesn't see. The
  Dockerfiles build plain `./cmd/<name>` with `CGO_ENABLED=0`; the flags here
  match. A new Dockerfile that builds a second binary (as management-api
  does) needs a line in `go_dependents`.
- A file-level build constraint (e.g. `_windows.go`) maps to its whole
  package; that over-rebuilds, which is safe.
- `TestAffectedServices` now runs `go list` for real, about 35s.
- Workflow and `infrastructure/azure/**`: PR gets `agent:needs-human`.

## Verify

- `go test -count=1 ./pkg/managementapi/...`, `go vet ./...`,
  `golangci-lint run ./...` from `src/`; `bash -n` on the script.
- This PR's own file list through the script.
- Mutation: drop each fail-safe rule in turn and see a test fail.
