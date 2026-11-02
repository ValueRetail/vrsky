# Build the images on the runner (#628)

## Open questions

None blocking. Two calls made without asking, both in the PR:
the cache scope (shared for Go, separate for the UI) and leaving
`build-push-acr.sh` on `az acr build`.

## Goal

Cut ACR Tasks build-minute cost to about zero without changing what gets
deployed. `build-images` in `.github/workflows/build-push.yml` builds each
image with Docker Buildx on the GitHub runner (free minutes, public repo) and
pushes it to `vrskyprodacr`. On 2026-10-29, 159 builds took 433 ACR Tasks
minutes; October's registry line was 467 NOK, about 61 of it the Basic SKU.
Expected saving: roughly 300-450 NOK a month.

## Acceptance criteria

1. `build-images` has no `az acr build`. Each matrix job runs `az acr login
   --name vrskyprodacr` before the build, then `docker/build-push-action` with
   `context: .`, the image's own Dockerfile, `platforms: linux/amd64`,
   `push: true`, tag exactly `vrskyprodacr.azurecr.io/vrsky/<img>:latest`.
2. management-api still gets `AGENT_VERSION=$(git describe --always --dirty)`
   (checkout keeps `fetch-depth: 0`).
3. `:latest` stays a single-platform manifest: `provenance: false`,
   `sbom: false`. Otherwise the tag becomes an image index and the digest the
   deploy reads is the index's.
4. The deploy job and Catch up are untouched: they still read the digest with
   `az acr repository show`, which does not depend on ACR Tasks run records.
5. `fail-fast: false`, the matrix and the job name stay.
6. A guard test (`TestBuildImagesBuildOnTheRunner`) pins 1-3 and fails on the
   old workflow.
7. `docs/operator/deploy.md` describes the new path and says the manual
   script stays on `az acr build`.

## Files

- `.github/workflows/build-push.yml` (the `build-images` job and one comment)
- `src/pkg/managementapi/nodeconfig_test.go` (new guard; `workflowFile` gets
  `uses` and `with`)
- `src/pkg/managementapi/dockerignore_guard_test.go`, `.dockerignore`
  (comments only)
- `docs/operator/deploy.md`, `docs/DEPLOYMENT_GUIDE.md`, `AGENTS.md`,
  `CLAUDE.md`, `PROJECT_STRUCTURE.md`, `infrastructure/azure/build-push-acr.sh`
  (wording, no behaviour)

## Approach

What the Dockerfiles look like (39 of them: 38 Go, one UI):
- Every `FROM` is Docker Hub (`golang:1.26-alpine`, `alpine:3.18/3.19`,
  `postgres:18-alpine`, `node:20-alpine`, `nginx:1.27-alpine`). None uses the
  ACR mirror, so the login does not have to come first today. It does, so a
  future mirrored `FROM` works.
- Context is the repo root; the root `.dockerignore` allowlist applies to
  Buildx the same way, and no `<Dockerfile>.dockerignore` exists (a guard
  already checks that). `.git` is not in the context, so `git describe` runs
  on the host in its own step.
- Go Dockerfiles: `COPY go.mod go.sum`, `go mod download`, `COPY src/`,
  `go build`. No BuildKit cache mounts anywhere.

Cache: `type=gha`, `mode=max`, `ignore-error=true`.
- Scope `image-go` for every Go image, `image-ui` for the UI. A shared scope
  fits the stage layout: the base image, the module download layer and the
  `COPY src/` layer are identical across Go images, so one warm entry serves
  all 35. Per-image scopes would store those layers 35 times against the 10 GB
  limit. Parallel writers to one scope race on the manifest, but each manifest
  lists the full chain of its own build, so the shared layers are in whichever
  one wins.
- What it does not give: the compiled packages. The final `RUN go build` layer
  differs per image and the Go build cache lives inside it, not in a cache
  mount. Cache mounts are not exported to `type=gha`, so a mount would only
  help within one runner, and each matrix job has its own. Rewriting 38
  Dockerfiles for that is out of scope; runner minutes are free, so the extra
  compile time costs nothing. A follow-up could use `buildkit-cache-dance` or
  one job building several images.
- `mode=max` also exports each image's builder layers, which include its Go
  build cache. If that fills 10 GB, GitHub evicts least-recently-used entries
  and the shared layers (used on every run) stay warm.

## Risks

- **Untested until main.** `build-images` runs only on push to main, so PR CI
  does not run the Buildx path. The first merge is the real test. Rollback is
  reverting the PR (the old `az acr build` step comes back as it was).
- **Cache eviction or a full 10 GB**: slower builds, never a failed one
  (`ignore-error`). A cold build compiles from scratch, like ACR did.
- **Docker Hub pulls** from 35 runners at once: rate limits are per IP and
  GitHub's runners are shared. ACR Tasks pulled from Docker Hub too. If it
  bites, the fix is a Docker Hub login or mirroring bases into ACR (now
  possible, the login comes first).
- **Index vs manifest**: handled by `provenance: false`, `sbom: false`.
- **Docker context default**: `build-push-action` defaults to the Git URL, not
  the checkout, so `context: .` is explicit and guarded.
- **Roles**: `AcrPush` is enough for the push. `Contributor` stays for the
  by-hand script and other reads; nothing is removed.
- **Time**: a cold Go image may take longer than 3 minutes on a 2-core
  runner. The 35-wide matrix still runs in parallel.

## How we verify it

- `go test -count=1 ./pkg/managementapi/...` in `src/`, including the new
  guard; the guard fails on main's workflow.
- `actionlint` clean on `build-push.yml`.
- A local `docker buildx build --platform linux/amd64` of `data-filter` from
  the repo root (no push).
- On the first merge to main: every `Build <img> in ACR` job pushes (look for
  the `pushing manifest for ...:latest` line), the deploy job's
  `digests:` listing shows a new digest per core image, and the smoke check
  passes. The Production check (drift) stays green afterwards.
