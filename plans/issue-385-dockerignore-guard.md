# Apply .dockerignore in the Dockerfile guard (#385)

No open questions.

## Goal

`TestMakefileDockerBuildUIContextHoldsDockerfileSources` checks that every
COPY/ADD source in `ui/Dockerfile` exists on disk, but not that the root
`.dockerignore` lets it into the build context. A narrowed allowlist
(`!ui` → `!ui/src`) passes the guard and breaks the ACR build on main.
Add a guard that filters the context the way Docker does, for every
Dockerfile built from the repo root.

## Acceptance criteria (from the issue)

- A guard test applies the root `.dockerignore` the way Docker does
  (allowlist `*`, `!` re-includes, last match wins, `**`, a directory
  excluded with a later `!` re-include of a child) and fails when a COPY/ADD
  source of any Dockerfile built from the repo root would be excluded.
- It covers every Dockerfile that `infrastructure/azure/build-push-acr.sh`,
  `build-push.yml` and the compose files build, not only the UI's.
- Mutations that must fail it: `!ui` → `!ui/src`; excluding `src/go.sum`;
  excluding `infrastructure/migrations`.
- `govulncheck ./...` is clean with the new dependency, and the PR says
  why it was added.
- The existing test keeps its behaviour.
- Backlog item 22 struck with `(#385)`.

## Which Dockerfiles

Every image builds with the repo root as context:

- `build-push-acr.sh` and the `build-images` job in `build-push.yml` run
  `az acr build --file <dockerfile> .`, where the Dockerfile is
  `ui/Dockerfile` or `src/cmd/<name>/Dockerfile`.
- Every `build:` in `docker-compose*.yml` has `context: .`.
- `make docker-build-ui` runs `docker build -f ../ui/Dockerfile ..` in
  `src/` (the existing test covers how that resolves).

The new test checks `ui/Dockerfile` and every `src/cmd/*/Dockerfile` (the
ACR paths), plus every Dockerfile a compose `build:` names. A compose build
whose context isn't the repo root, or a Dockerfile-specific
`<Dockerfile>.dockerignore` next to one of them (BuildKit reads it instead
of the root file, ACR doesn't), fails the test with "update this test"
rather than being skipped.

## Approach

- Matcher: `github.com/moby/patternmatcher` v0.6.1, the code Docker's CLI
  and BuildKit use. `ignorefile.ReadAll` reads the file the way Docker does
  (comments, whitespace, `filepath.Clean`, leading `/`), and
  `MatchesOrParentMatches` gives the "last match wins, a parent's match
  counts" result. It has no dependencies of its own and is used only in a
  test file, so nothing ships in a binary. Writing our own matcher would
  mean re-deriving `**` and parent-match rules and keeping them in step
  with Docker; that is how the guard would drift from what ACR does.
- Factor the COPY/ADD parsing out of the existing test into
  `dockerfileCopySources(t, dockerfile)`: same rules (skip `--from`, skip
  flags, the last field is the destination, hard stop on a line
  continuation or a remote ADD). The existing test calls it and keeps its
  glob-against-context check and its "found no COPY sources" stop.
- Per source: glob it from the repo root. It passes when at least one
  match is a file the matcher lets through, or a directory holding at
  least one such file (walk stops at the first). That mirrors BuildKit: a
  wildcard with no surviving match or a missing directory fails the COPY.
  A directory where every file is ignored would copy as empty, which is
  just as broken, so it fails too.
- New file `src/pkg/managementapi/dockerignore_guard_test.go`:
  the helper, the guard test, and table tests for the matcher setup
  (Docker's documented examples plus this repo's allowlist shape) so the
  test's use of the library is pinned.

## Files

- `src/pkg/managementapi/dockerignore_guard_test.go` (new)
- `src/pkg/managementapi/makefile_guard_test.go`: use the shared parser;
  the doc comment no longer points at backlog item 22.
- `src/go.mod`, `src/go.sum`: `github.com/moby/patternmatcher`.
- `plans/backlog.md`: strike item 22.
- `plans/issue-385-dockerignore-guard.md` (this file).

## Risks

- It reads the working tree, not `git ls-files`. A git-ignored file on a
  dev machine could satisfy a COPY that a clean checkout can't. CI runs on
  a clean checkout, which is where it counts; the existing guard has the
  same property.
- "At least one file gets through" doesn't catch a pattern that drops part
  of a copied directory (say `ui/src/**/*.tsx`). Checking every tracked file
  would flag the intended exclusions (`src/Makefile` under `COPY src/`,
  `ui/.env.example`), so that stays out of scope.
- `az acr build` packs the context with the Azure CLI's own code, not
  BuildKit. Production images build fine with the current allowlist, so it
  reads the same syntax for what we use today, but this test can't prove
  the two agree on every corner case.

## Verification

From `src/`:

- `go test -count=1 ./pkg/managementapi/...`
- `go vet ./...`, `golangci-lint run ./pkg/managementapi/...`, `gofmt -l`
- `govulncheck ./...`
- `go mod tidy` leaves no diff
- Mutation run (by a separate test-author agent): `!ui` → `!ui/src`,
  `src/go.sum` excluded, `infrastructure/migrations` excluded each fail
  the new test; `.dockerignore` restored after.
- CI green on the PR.
