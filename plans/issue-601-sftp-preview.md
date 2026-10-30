# SFTP sample preview skips temp names (#601)

Backlog item 139. Open questions: none.

## Goal

The SFTP sample-data handler (`src/cmd/sftp-consumer/server.go`) picks the
first file that matches `file_pattern`. It now skips the temp names the
poller skips since #590, so the builder preview shows a file that will be
published.

## Acceptance criteria

1. With no pattern (or `*`, `*.json`), a dotfile, `~` file, `.tmp` or
   `.part` file is not sampled; a real file is.
2. A `file_pattern` that names temp names (`*.part`, `.*`, `~*`) still
   samples them. Same rule as the poller: `isTempName` and `patternNamesTemp`.
3. Only temp names in the folder: the existing "no files to sample yet"
   answer.
4. Table-driven test that fails without the change.
5. Backlog item 139 struck with `(#601)`.

## Touches

`src/cmd/sftp-consumer` only (server.go, a new test file), `docs/connectors/sftp.md`
(one sentence), `plans/backlog.md`.

## Decisions

- Reused the poller's two helpers instead of a copy, so the preview cannot
  drift from the poller.
- The test lists files in a fixed order (a small wrapper around the fake),
  because the fake's map order is random and "first match" would not be
  pinned down.
- The test-connection handler only lists up to 20 names and does not sample,
  so it is unchanged.
