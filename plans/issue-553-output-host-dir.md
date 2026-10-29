# #553: the file destination's host folder is fixed to data/output

Backlog item 86, found in #422.

## Problem

#422 (PR #474) added `VRSKY_FILES_HOST_DIR`, the host folder behind the
file connectors' `/data/input`. file-producer still mounts
`./data/output:/data/output`, so the destination always writes into the
repository. A Windows user can point the source at their home folder but
not the destination.

## Approach

- New `.env` variable `VRSKY_FILES_OUTPUT_HOST_DIR`, default
  `./data/output`. Like the input one it is only a mount source.
- file-producer mounts `${VRSKY_FILES_OUTPUT_HOST_DIR:-./data/output}` at
  the fixed target `/data/output`, which is `FILE_OUTPUT_DIR`. It replaces
  the hard-coded `./data/output` line, so nothing else mounts there.
- The read-only input mount and `HOST_HOME: "/data/input"` stay as they
  are. `getHostOwner` keeps probing `/data/input`, so #474's root-owned
  fallback is untouched. No Go code changes outside the guard test.
- `.env.example` lists the variable with an empty value, so an unedited
  copy resolves to the same config as no `.env` (#381's rule).
- `docker-compose-files.yml` (the old standalone file-pipeline demo, used
  only by two shell scripts under `test/`) keeps its fixed mounts. It never
  took `VRSKY_FILES_HOST_DIR` either.

## Acceptance criteria

1. file-producer mounts `${VRSKY_FILES_OUTPUT_HOST_DIR}` at a fixed absolute
   Linux path, and that path is its `FILE_OUTPUT_DIR`.
2. No other file-producer mount has `/data/output` (or `FILE_OUTPUT_DIR`) as
   its target; the guard fails on `origin/main`'s compose file.
3. The #422 checks still hold: input mount at `/data/input`, `HOST_HOME`
   equal to it, so the owner probe sees the same folder as before.
4. `docker compose config` with `VRSKY_FILES_OUTPUT_HOST_DIR` set to
   `C:\Users\x\out`, `C:/Users/x/out` and `/c/Users/x/out` (env and env
   file) gives target `/data/output`.
5. `.env.example` has the variable, empty, and resolves to the same config
   as no `.env`.
6. `windows.md`, `install.md` and `docs/connectors/file.md` say how to pick
   the output folder; backlog item 86 struck with (#553).

## Files

- `docker-compose.yml`
- `.env.example`
- `src/pkg/managementapi/compose_files_mount_guard_test.go`
- `docs/operator/windows.md`, `docs/operator/install.md`,
  `docs/connectors/file.md`
- `plans/backlog.md`

## Risks

- Someone with output files in `data/output` who sets the variable sees an
  empty folder; the old files stay where they were. Docs say so.
- Owner on a Linux host with different owners for the two folders: files
  follow the input folder's owner, as today.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -count=1 ./pkg/managementapi/`. The guard against `origin/main`'s
compose file must fail; a mutation run on the guard. The `docker compose
config` runs above. Never `docker compose down -v`.
