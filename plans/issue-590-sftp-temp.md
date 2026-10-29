# SFTP consumer skips temp names (#590)

Backlog item 117. Open questions: none. Calls made without asking are under
"Decisions".

## Goal

The SFTP consumer (`src/cmd/sftp-consumer`) leaves temp names alone until
they are renamed: the same names the file consumer skips since #374. A file
uploaded as `x.csv.part` and renamed to `x.csv` is published once, as
`x.csv`. Before, a temp name that sat unchanged for the settle gap (#562) was
published under that name, and again under its final name.

## Acceptance criteria

1. A name that starts with `.` or `~`, or ends in `.tmp` or `.part` (any
   case), is not published, not tracked in the pending sightings, not counted
   as waiting, and not touched by `after_action`.
2. `file_pattern` can still ask for temp names, but only by naming them (see
   "Decisions"). Empty, `*`, `*.csv` and the like do not.
3. `x.part` uploaded, then renamed to `x.csv`: published once, as `x.csv`,
   after the usual two looks, whether it was renamed at once or sat under the
   temp name for several polls.
4. The done set (#538) and the stability check (#562) are unchanged: a final
   name still needs two unchanged looks, is published once, and is not
   published again, also after a restart.
5. Table-driven tests that fail without the change. A mutation run on the
   rule. `docs/connectors/sftp.md` says which names are skipped.
6. `plans/backlog.md` item 117 struck with `(#590)`.

## Files

- `src/cmd/sftp-consumer/service.go`: `isTempName`, `patternNamesTemp`, one
  check in `pollOnce`.
- `src/cmd/sftp-consumer/tempname_test.go` (new).
- `docs/connectors/sftp.md`, `plans/backlog.md`.

Nothing outside `src/cmd/sftp-consumer` in Go. `src/cmd/file-consumer` is not
changed and not imported.

## Decisions

- The rule is copied from `isTempName` in `src/cmd/file-consumer/service.go`
  with a comment pointing at it, not extracted into a shared package: that
  would make file-consumer import it, and the issue forbids changing it. The
  test table in `TestTempName_Rule` lists the same names so a drift shows.
- `file_pattern` interaction: a temp name is let through only when the pattern
  matches it **and** the pattern is itself in the temp form (starts with `.`
  or `~`, or ends in `.tmp` or `.part`: `*.part`, `.*`, `~*`, `x.csv.part`).
  Reason: "explicitly matches" should mean the operator asked for temp names.
  `*` and `*.csv` are common and match `x.csv.part` / `.hidden` by accident of
  the glob (`*` matches a leading dot in `path.Match`); letting those win would
  leave most connections with a pattern unprotected. The pattern still has to
  match the name first, as before, and stays case-sensitive.
- The check sits after the done-set and pattern checks, before the stability
  check. A done temp name (published before this change) is skipped by the
  done check as before; it is never added.
- A skipped temp name is not counted as waiting, so the poller keeps to its
  interval instead of looking every settle gap for a file that may never be
  renamed. Renaming changes the name, so the renamed file is a new file with
  its first sighting at the next poll: it goes out after the usual two looks.
- Debug log line for a skipped name (as the file consumer does); the name
  can be listed every poll, so not info.
- Behaviour change to note in the docs: a connection with no `file_pattern`
  that relied on getting `.part` files must now set `file_pattern: "*.part"`.

## Risks

- A final name that looks like a temp name (`.csv` exports named like a
  dotfile, a `~` prefix, a `.tmp` extension used for real data) is no longer
  published unless the pattern names it. Same as the file consumer; the docs
  say so.
- A pattern in a form the rule does not recognise (`[.]*`) does not let
  dotfiles through. Accepted; `.*` does.

## Verification

From `src/`: gofmt, `go build ./...`, `go vet ./...`, `golangci-lint run
./cmd/sftp-consumer/...`, `go test -race -count=3 ./cmd/sftp-consumer/...`
(with `VRSKY_TEST_POSTGRES_URL`), `make lint-tenant`. The new tests fail
before the change (listed in the PR). Mutation run on the rule: dropping the
dot prefix, the tilde prefix, `.part`, `.tmp`, the case fold; the pattern
never winning, any pattern winning, a pattern always winning; the skip
removed. Each fails at least two tests.
