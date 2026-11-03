# Pull base images from mirror.gcr.io (#692)

No open questions.

## Goal

The main build for #681 (run 41604470984) failed: nine image builds got
`429 Too Many Requests` from `registry-1.docker.io` on `golang:1.26-alpine`,
`alpine:3.18` and `alpine:3.19`, with the Docker Hub login in place. The
authenticated quota was spent by the day's runs. Pull the bases from
`mirror.gcr.io`, Google's public pull-through cache of Docker Hub, which has
no such limit for us. Same tags, so the same base images.

## Acceptance criteria (from the issue)

- Every Dockerfile production builds uses `mirror.gcr.io/library/<image>:<tag>`
  with the tag it has today.
- `docker/setup-buildx-action` in `build-push.yml` runs BuildKit from
  `mirror.gcr.io/moby/buildkit:buildx-stable-1`.
- The same in other workflows and compose files CI uses, where it is a plain
  base-image reference.
- A guard test fails when a Dockerfile adds a `FROM` on bare Docker Hub.
- The operator docs mention the mirror.

## Affected files

- `src/cmd/*/Dockerfile` (39) and `ui/Dockerfile`: the `FROM` lines.
- `.github/workflows/build-push.yml`: `driver-opts` on setup-buildx, the
  comments, and the `postgres:16-alpine` service container of the Go tests.
- `.github/workflows/integration.yml`, `e2e-nightly.yml`: `golang:1.26` and
  `busybox` in `docker run` / `docker pull`.
- `.github/workflows/dr-drill.yml`: its two `postgres:18-alpine` service
  containers.
- `docker-compose.yml`: the Docker Hub official images (`nats`, `postgres`,
  `rabbitmq`, `traefik`), and the third-party broker images integration.yml
  pulls (`pgsty/minio`, `pgsty/mc`, `fsouza/fake-gcs-server`, `atmoz/sftp`,
  `apache/kafka`). Added after #687's integration job hit 429 on them.
- `.github/workflows/build-push.yml`: promtool's `prom/prometheus:v3.5.0`.
- `src/pkg/managementapi/nodeconfig_test.go`: new guard test.
- `docs/operator/deploy.md`: a paragraph on the mirror.

## Approach

1. Checked that the mirror serves every tag (HEAD on
   `https://mirror.gcr.io/v2/<repo>/manifests/<tag>`, all 200) and that the
   index digests match Docker Hub's (`docker buildx imagetools inspect` for
   golang:1.26-alpine, alpine:3.19, nginx:1.27-alpine: identical).
2. Rewrite the `FROM` lines with sed; no other Dockerfile change.
3. `driver-opts: image=mirror.gcr.io/moby/buildkit:buildx-stable-1` on
   setup-buildx. The Docker Hub login stays: it costs nothing and covers any
   pull that still goes to Docker Hub.
4. Guard test `TestDockerfilesPullBaseImagesFromTheMirror`: walks the repo
   for Dockerfiles (skipping `.git`, `node_modules` and dot-directories),
   parses each `FROM` (with `--platform=`, `AS name`), lets through `scratch`,
   earlier stage names and images on another registry host, and fails on a
   Docker Hub reference (no registry host, or `docker.io/…`) or an
   unresolvable `$VAR` base. It also checks the setup-buildx `driver-opts`.

## Not changed

- The rest of the third-party images in `docker-compose.yml` (grafana,
  prom, otel, mailpit, httpbin …): CI does not start them.
- `docker-compose-files.yml`: not used by CI.
- `grafana/k6` (load-smoke.yml): one pull per run, not asked for.
- `AGENTS.md`'s local promtool command: a developer machine, not CI.
- `infrastructure/azure/postgres-tls.sh` (`image: postgres:18-alpine`): the
  cluster pulls that one, not CI, and it is production infrastructure.
- `infrastructure/azure/build-push-acr.sh`: builds in ACR from the same
  Dockerfiles, so it picks up the mirror without a change.

## Risks

- mirror.gcr.io only serves what it has cached; a new tag nobody has pulled
  through it yet can 404 for a while. Every tag in use today is there. If a
  future bump hits that, the build fails loudly and the tag can be pulled from
  Docker Hub for that one change.
- Google could change or retire the mirror. Reverting is one sed.
- Changing `src/cmd/*/Dockerfile` makes `affected.sh` rebuild every Go image
  (and `ui/Dockerfile` the UI), so this merge rebuilds and deploys all images.
  That is wanted here: it also replaces the images #681 failed to build.

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`.
- `go test -count=1 -run 'TestDockerfilesPullBaseImagesFromTheMirror|TestDockerfilesRetryModuleDownload|TestBuildImagesBuildOnTheRunner' ./pkg/managementapi/`
  passes; the same test fails against main's Dockerfiles.
- Local `docker build` of `src/cmd/visma-consumer/Dockerfile` and
  `ui/Dockerfile` resolves the mirror references.
