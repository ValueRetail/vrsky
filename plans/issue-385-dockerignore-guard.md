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
  test file, so nothing ships in a binary (`go list -deps ./...` doesn't
  list it; only `-test` does). Writing our own matcher would mean
  re-deriving `**` and parent-match rules and keeping them in step with
  Docker; that is how the guard would drift from what ACR does.
- Factor the COPY/ADD parsing out of the existing test into
  `dockerfileCopySources(t, dockerfile)`: same rules (skip `--from`, skip
  flags, the last field is the destination, hard stop on a line
  continuation or a remote ADD). The existing test calls it and keeps its
  glob-against-context check and its "found no COPY sources" stop.
- The file list is the tracked files (`git ls-files`), not the working
  tree, so a git-ignored local file (a `docker-compose.override.yml`, a
  stray build output) can't change the result. Without git the test skips
  with a message; if git is there and fails, the test fails.
- Per source: a tracked file is covered when the source (with
  `filepath.Match` wildcards, as Docker matches it) matches the file or one
  of its parent directories. Every covered file must survive the
  `.dockerignore`, apart from `dockerignoreKeepOut`, and at least one must.
- `dockerignoreKeepOut` is a commented list in the test of tracked files
  that a COPY covers but the `.dockerignore` leaves out on purpose. Today
  that's the 15 files from the `src/*` rule that the 32 `COPY src/ ./`
  Dockerfiles don't need: `src/.golangci.yml`, `src/.govulncheck-allow`,
  `src/Makefile` and `src/test/` (12 files). An entry that no longer
  excuses anything fails the test, so the list can't go stale.
- New file `src/pkg/managementapi/dockerignore_guard_test.go`:
  the helpers, the guard test, and table tests for the matcher setup
  (Docker's documented examples plus this repo's allowlist shape) so the
  test's use of the library is pinned.

### Review round 1

The first version passed a source when at least one covered file got
through. Thirty-two Dockerfiles use `COPY src/ ./`, so `src/go.mod` alone
satisfied them, and these lines passed the guard while each would break a
production build: `src/cmd/sitoo-consumer`, `src/pkg/sdk`,
`ui/src/**/*.tsx`, `ui/package.json` (`ui/package-lock.json` still matched
`ui/package*.json`). The rule is now "every covered tracked file", and those
four are mutation cases.

## Files

- `src/pkg/managementapi/dockerignore_guard_test.go` (new)
- `src/pkg/managementapi/makefile_guard_test.go`: use the shared parser;
  the doc comment no longer points at backlog item 22.
- `src/go.mod`, `src/go.sum`: `github.com/moby/patternmatcher`.
- `plans/backlog.md`: strike item 22.
- `plans/issue-385-dockerignore-guard.md` (this file).

## Risks

- Changing `src/go.mod` makes `affected.sh` treat every Go image as
  changed, so the merge rebuilds every Go image and rolls every service and
  connector. Nothing new is linked into a binary. Merge while the cluster
  is parked.
- `az acr build` packs the context with the Azure CLI's own Python matcher,
  not BuildKit. Production images build fine with the current allowlist,
  so it reads the same syntax for what we use today, but this test can't
  prove the two agree on every corner case.
- Untracked files that a build needs (none today) would be reported as
  missing. That's the right call for CI, which builds from a clean checkout.

## Verification

From `src/`:

- `go test -count=1 ./pkg/managementapi/...`
- `go vet ./...`, `golangci-lint run ./pkg/managementapi/...`, `gofmt -l`
- `govulncheck ./...`
- `go mod tidy` leaves no diff
- Mutation run on the real `.dockerignore`, restored after each: `!ui` →
  `!ui/src`, `src/go.sum`, `infrastructure/migrations`,
  `src/cmd/sitoo-consumer`, `src/pkg/sdk`, `ui/src/**/*.tsx` and
  `ui/package.json` each fail the new test. The old guard still passes
  `!ui/src`. Dropping a keep-out entry, or adding a stale one, fails too.
- CI green on the PR.
