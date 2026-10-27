# ui-prod exits at once when run locally (#377)

No open questions.

## Goal

`docker compose -f docker-compose.ui.yml --profile prod up ui-prod` builds
since #372, but the container stops right away: the image runs nginx as the
`nginx` user (uid/gid 101), and nginx can't create `/var/run/nginx.pid`
(`Permission denied`). Production doesn't see this because
`infrastructure/kubernetes/ui/deployment.yaml` mounts emptyDirs at
`/var/run`, `/var/cache/nginx` and `/tmp`, with `fsGroup: 101`. Give the
compose service the same writable paths.

## Acceptance criteria (from the issue)

- `ui-prod` from `docker-compose.ui.yml` starts, stays up and serves the UI
  on its published port: HTTP 200 for `/` and `/index.html`.
- The fix lives in `docker-compose.ui.yml`. `ui/Dockerfile` builds the
  production image and is not changed.
- A guard test fails if the compose service loses whatever makes the pid
  path writable.
- `plans/backlog.md` item 21 struck with `(#377)`.

## Files

- `docker-compose.ui.yml`: a `tmpfs` list on `ui-prod` only.
- `src/pkg/managementapi/compose_ui_guard_test.go` (new): the guard test.
  Not `makefile_guard_test.go`, which #373 is editing.
- `plans/backlog.md`: strike item 21, append `(#377)`.
- `plans/issue-377-ui-prod-pid.md` (this file).

## Approach

- Add to `ui-prod`:
  ```yaml
  tmpfs:
    - /var/run:uid=101,gid=101
  ```
  `nginx.conf` sets `pid /var/run/nginx.pid`, so that's the one path that
  fails. Mode stays the tmpfs default (1777), which the nginx user can write
  regardless; the uid/gid make the owner match production's fsGroup.
- `/var/cache/nginx` is already owned by nginx in the image (the Dockerfile
  chowns it), and the compose root filesystem is writable, so it doesn't need
  a mount here. Production needs one only because it runs with
  `readOnlyRootFilesystem: true`. `/tmp` is world-writable in the image.
  Confirm both by running the container: nginx creates its temp dirs under
  `/var/cache/nginx` at startup and would fail there too if it couldn't.
- No `read_only: true` on the compose service: it would make the local run
  closer to production, but it's a bigger change than the issue asks for.
  Noted in the PR.
- The guard test parses `docker-compose.ui.yml` with `gopkg.in/yaml.v3`
  (already a dependency) and checks `services.ui-prod` mounts something
  writable at `/var/run` (a tmpfs entry, or a volume targeting it).

## Risks

- None for production: CI and the ACR build don't use this compose file
  (`build-push.yml` runs `az acr build -f ui/Dockerfile .`), and the
  Dockerfile and manifests are unchanged.
- `tmpfs` with `uid=`/`gid=` options needs a Linux daemon (Docker Desktop is
  one). Compose's short `tmpfs` syntax passes the options through to the
  mount.

## Verification

- Before: reproduce the exit and the `Permission denied` log line.
- After: the container stays up past the healthcheck start period,
  `curl` gets 200 for `/` and `/index.html` on the published port, and the
  container reports healthy. Use a throwaway project (`-p vrsky-issue377`)
  and `docker compose ... down` afterwards.
- `go test -count=1 ./pkg/managementapi/...`, `go vet ./...`,
  `golangci-lint run ./pkg/managementapi/...`, `gofmt -l`.
- Mutation: drop the tmpfs, the guard fails; restore, it passes.

## What happened (2026-10-27)

- The Go checks, `docker compose config` and the mutation run are done
  (see the PR).
- The build couldn't run: Docker Desktop's daemon hangs on every pull from
  Docker Hub (`load metadata for docker.io/library/node:20-alpine:
  DeadlineExceeded`), other sessions' pulls included, while containers
  reach the registry fine. So neither the `Permission denied` repro nor the
  200s have been seen on this branch yet. The tmpfs itself was checked with
  a local busybox image: `--tmpfs /var/run:uid=101,gid=101` gives
  `drwxrwxrwt 101 101` and uid 101 can create `/var/run/nginx.pid` there.
- Still to do before merge: build and run `ui-prod` once the daemon can pull,
  and check `/` and `/index.html` return 200.
