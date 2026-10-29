# #495: the alert-rules legs in a file under infrastructure/

## Open questions

None blocking. The PR edits `.github/workflows/build-push.yml`, so it waits
for a human (`agent:needs-human`) one last time; after it, a PR that adds a
rule test group touches only `infrastructure/`.

## Goal

Since #464 every new test group in `infrastructure/prometheus-rules-test.yml`
must also be listed in a leg of the `alert-rules` matrix in
`build-push.yml`. Move the lists into
`infrastructure/prometheus-rules-test-legs.yml`. The workflow keeps the leg
names (`1/4` … `4/4`); each leg reads its groups from the file.

Acceptance criteria:

- Every group in `prometheus-rules-test.yml` is in exactly one leg of the
  legs file, and the file's legs are exactly the workflow's matrix legs.
- Each leg still runs `check rules`, builds an anchored `--run` from its
  groups, and fails when the JUnit report lists fewer groups than it named.
- A leg fails loudly when the legs file is missing, when it has no entry
  for the leg, when the entry is empty, or when a line under it is not a
  plain `  - <group>` item.
- `Go Tests` still needs `alert-rules`, `plan` still needs it, no
  `continue-on-error`, no step-level `if`, every `docker run` mounts
  `infrastructure` at `/cfg` read-only.
- Check names stay `Alert rules (promtool 1/4)` … `4/4` and the groups
  stay in the legs they are in today, so leg times do not move.

## Affected files

- `infrastructure/prometheus-rules-test-legs.yml` (new)
- `.github/workflows/build-push.yml` (alert-rules matrix and step)
- `src/pkg/managementapi/ruletestlegs_guard_test.go`
- `AGENTS.md`, `docs/DEPLOYMENT_GUIDE.md`, `plans/backlog.md` (item 80)

## Approach

- Matrix: `leg: ['1/4', '2/4', '3/4', '4/4']` instead of `include` with
  group lists. The job name and the check names do not change.
- The step reads the file with a short POSIX `awk` script instead of `yq`.
  `yq` is on ubuntu-latest, but not on a developer's Mac, and the guard
  runs the step itself; with `awk` it runs the real parse everywhere (BSD
  awk locally, mawk on the runner). The format is strict: a top-level
  `<leg>:` key per leg, then `  - <group>` lines. Comments and blank lines
  are skipped; any other line under the leg fails the step. The guard
  parses the same file with yaml.v3 and checks the step's `--run` selects
  exactly that leg's groups, so the two parses cannot drift apart.
- Explicit checks rather than relying on `bash -e`: file present, awk
  succeeded, at least one group.

## Guard (`TestRuleTestGroupsRunOnce`)

- Static checks moved into `ruleLegProblems(rulesTest, legsFile, workflow)`
  so they can be fed mutated copies. Table of mutations, each must report a
  problem: a group missing from the legs file, a group in two legs, a leg
  left empty, the legs file deleted, a leg in the file the workflow does
  not run, `continue-on-error` on the job, `continue-on-error` on the step,
  `include` added to the matrix.
- Running the step (fake docker, as before) from the real checkout for every
  leg, plus from a temp copy of `infrastructure/` with: the file missing,
  the leg's entry empty, the leg's entry gone, a stray line under the leg.
  Each must fail.
- Every `docker run` the step makes mounts `…/infrastructure:/cfg:ro`.

## Verify

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -count=1 ./pkg/managementapi/` from `src/`.
- promtool locally with the CI docker commands.
- Manual mutation run on the real files (the five from the issue), guard
  red each time.
- PR CI: each leg prints "ran N of the N test groups", and its job time is
  compared with a recent main run.

## Risks

- awk dialects: only POSIX features are used (no `[[:space:]]`, no gawk
  extensions); `/dev/stderr` works in BSD awk, mawk and gawk. The guard runs
  the script under the local awk, CI under mawk.
- Someone writes the legs file in a YAML style the awk does not read (flow
  list, quoted key, trailing comment). The leg then fails loudly in CI and
  the guard fails locally, with the line it could not read.
