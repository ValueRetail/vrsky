# #422: no way to mount a Windows home folder for the file connectors

Backlog item 34, found in review of #381.

## Problem

`docker-compose.yml` mounted `${HOME:-./data/input}` at
`${HOME:-/data/input}` in `file-consumer` and `file-producer`, and set
`HOST_HOME` to the same value. Source and target were the same string. On
Windows that string is `C:\Users\you`, which the Linux engine refuses as a
target ("mount path must be absolute"), so setting `HOME` broke the file
connectors. Git Bash doesn't help: it hands native programs like
`docker.exe` its `HOME` converted to `C:\Users\you`.

## What the mount actually does today

Since cacfa8c4 (`pkg/tenantpath`) every file path resolves inside the
connector's base dir: `/data/input/<workspace>/…` for the source,
`/data/output/<workspace>/…` for the destination. A path under the mounted
home (`/Users/you/x`, or `~/x` expanded to it) is refused. So on macOS and
Linux the whole home directory was mounted into both containers but no
pipeline could reach it. `windows.md` still said "a path typed in the editor
means the same thing inside the container", which stopped being true then.

## Approach

- New `.env` variable `VRSKY_FILES_HOST_DIR`, default `./data/input`. It is
  only ever a mount source.
- Target fixed at `/data/input`, the source's base dir, so the chosen folder
  is the one file pipelines read (`<folder>/<workspace id>/…`). A separate
  target such as `/data/home` would mount it where tenantpath refuses every
  path, which is the dead mount we have now.
- `HOST_HOME` becomes the literal `/data/input`, so `~/x` in a source's path
  means `<folder>/x` and then the workspace's copy of it. (On the destination
  `~` expands to `FILE_OUTPUT_DIR`, `/data/output`, as before.)
- `file-consumer` had both `./data/input:/data/input` and the home mount; the
  new line replaces both (with `HOME` unset they were the same mount twice).
- `file-producer` keeps a mount of the same folder at the same target,
  read-only, so `HOST_HOME` still exists there for its allowlist and owner
  lookup. Its writes still go to `./data/output`; a variable for that is
  backlog item 79.
- Review finding: `getHostOwner` follows `HOST_HOME`'s owner when it differs
  from `FILE_OWNER_UID`. That was the user's `$HOME`; now it is the host's
  `data/input`, which dockerd creates as root:root on a fresh Linux clone, so
  every output would turn root-owned. A root-owned folder is no longer taken
  as the host user; `FILE_OWNER_UID/GID` stand then. The decision moved into
  `hostOwner` so a test can feed it a root-owned probe.
- `.env.example` lists the variable with an empty value, so an unedited copy
  still resolves to the same config as no `.env` (#381's rule).

Behaviour change: on macOS and Linux the home directory is no longer mounted
into the two containers. Nothing could read or write it through a pipeline,
so this only removes exposure.

No change under `infrastructure/`: the Kubernetes manifests don't use
`HOME` or `HOST_HOME`.

## Acceptance criteria

1. In both file services every mount target is a fixed absolute Linux path,
   not read from a variable; the guard fails on the old compose file.
2. `HOST_HOME` is a fixed Linux path and equals the target of the
   `VRSKY_FILES_HOST_DIR` mount; in file-consumer that target is
   `FILE_CONSUMER_BASE_DIR`.
3. `docker compose config` with `VRSKY_FILES_HOST_DIR` set to `C:\Users\x\files`,
   `C:/Users/x/files` and `/c/Users/x/files` (env and env file) gives target
   `/data/input`.
4. `.env.example` resolves to the same config as no `.env`.
5. A root-owned host folder with `FILE_OWNER_UID=1000` gives owner 1000, not
   root.
6. `windows.md`, `install.md` and the file connector page say how to pick the
   folder; backlog item 34 struck with (#422).

## Files

- `src/cmd/file-producer/main.go` (`getHostOwner` split into `hostOwner` and
  `probeHostHomeOwner`), `src/cmd/file-producer/owner_test.go` (new)

- `docker-compose.yml`
- `.env.example`
- `src/pkg/managementapi/compose_files_mount_guard_test.go` (new)
- `docs/operator/windows.md`, `docs/operator/install.md`, `docs/connectors/file.md`
- `plans/backlog.md`

## Verify

`go test -count=1 -run 'TestComposeFilesMount|TestEnvExample' ./pkg/managementapi/`,
the same test against `origin/main`'s compose file (must fail), and the
`docker compose config` runs above. Never `docker compose down -v`.
