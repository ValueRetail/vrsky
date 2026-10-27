# Untrack the Go binaries committed in `src/` (#360)

Checked in the repo on 2026-10-27.

## Open questions

None blocking. Decided without asking (see Approach): explicit anchored
names in `.gitignore` plus a guard test, instead of an allowlist for `src/`.

## Acceptance criteria (from the issue)

1. `src/remote-agent`, `src/vrsky-cli` and `src/spill-e2e` are removed from
   the index (`git rm --cached`), not from anyone's working tree.
2. `.gitignore` keeps them, and any other `go build` output in `src/` named
   after a `cmd/` directory, out of the repo, without hiding real source
   files. `git status` stays clean after `make build` and after a plain
   `go build ./cmd/<name>` in `src/`.
3. Nothing in the repo reads those paths (scripts, Dockerfiles, workflows,
   docs); if something does, point it at `bin/` or a fresh build.
4. `plans/backlog.md` item 11 struck with `(#360)`.

History rewriting is out of scope; the blobs stay in old commits.

## What I found

- The three files are Mach-O executables, 34 to 64 MB, last touched by
  `2a9f4a03` (agent groups, 2026-09-28).
- `.gitignore` already has an explicit `/src/<name>` list for `go build`
  outputs, but it has drifted: of the 45 directories in `src/cmd/`, these
  are missing: `alert-responder`, `lint-openapi`, `lint-tenant-filter`,
  `remote-agent`, `spill-e2e`, `vrsky-agent`, `vrsky-cli`. The list also has
  entries for commands that no longer exist (`converter`, `filter`).
- `make build` writes to `src/bin/` (`go build -o $(BIN_DIR)/ ./cmd/...`),
  already ignored by `bin/`.
- Nobody reads the committed paths:
  - `.dockerignore` already excludes everything at `src/` root except
    `go.mod`, `go.sum`, `cmd/`, `pkg/`, `internal/`.
  - `dr-drill.yml` builds `vrsky-cli` fresh with
    `go build -o /usr/local/bin/vrsky-cli ./cmd/vrsky-cli`.
  - `build-push.yml`, the scripts, docs and `plans/` mention
    `remote-agent` only as a service or image name.

## Files touched

- `src/remote-agent`, `src/vrsky-cli`, `src/spill-e2e`: index only.
- `.gitignore`: the `/src/<name>` block rewritten as one sorted list, one
  line per `src/cmd/` directory.
- `src/pkg/managementapi/gitignore_test.go` (new): guard test.
- `plans/backlog.md`: item 11 struck.

## Approach

1. `git rm --cached` the three files. They stay on disk here. Other
   checkouts lose them on their next pull, which is fine: they are build
   outputs and `make build` or `go build` recreates them.
2. `.gitignore`: one anchored line per command, `/src/<name>`, for every
   directory in `src/cmd/`. Drop the stale `converter` and `filter` lines.
   - Why explicit names and not a pattern: gitignore cannot say "files
     only", and every pattern that would catch any binary at `src/` root
     (`/src/*` with `!/src/*/` and re-includes for `Makefile`, `go.mod`,
     `.golangci.yml` and so on) is an allowlist. It would silently hide the
     next real file someone adds at `src/` root (a `tools.go`, a README, a
     linter config). An anchored `/src/<name>` matches exactly one path,
     which today is never a directory or a source file.
   - The cost of explicit names is drift, which is how this bug happened.
     The guard test closes that.
3. Guard test (`TestGitignoreCoversCmdBinaries`), next to the other repo
   guard tests in `pkg/managementapi`: for every directory in `src/cmd/`,
   the root `.gitignore` must have the line `/src/<name>`; and no
   `/src/<name>` line in that block may name an existing directory or a
   tracked-looking source path at `src/` root (`go.mod`, `Makefile`, ...),
   so the list can never hide real source.
4. Strike backlog item 11.

## Risks

- An ignore line that hides real source: only if a directory or file at
  `src/` root were named like a command. The guard test fails on a
  `/src/<name>` entry that matches an existing directory at `src/` root.
- `go build` inside `src/cmd/<name>/` writes `src/cmd/<name>/<name>`; only
  `management-api` is covered there today. Out of scope (the issue is about
  `src/`); noted in the PR.
- Pulling this removes the three files from other people's working trees.
  Expected for untracked build outputs.

## Verification

- An independent checker (subagent) in this worktree:
  - `git status` clean after `cd src && make build`, after
    `go build ./cmd/remote-agent`, and after one other `go build ./cmd/<x>`.
  - `git check-ignore -v src/remote-agent src/vrsky-cli src/spill-e2e`
    reports the new rules.
  - `git ls-files | git check-ignore --stdin --no-index` prints nothing.
  - `go build ./...` and `go vet ./...` pass.
- `go test -count=1 ./pkg/managementapi/ -run TestGitignoreCoversCmdBinaries`
  passes, and fails when a cmd line is removed from `.gitignore`.
- `golangci-lint run ./pkg/managementapi/...`, `gofmt`.
