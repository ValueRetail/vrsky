# make docker-build-ui can't build the UI (#369)

No open questions.

## Goal

`make docker-build-ui` passes `../ui` as the build context, but
`ui/Dockerfile` copies `ui/...` paths from the repo root, so the build fails
at the first `COPY` (`"/ui/nginx.default.conf": not found`). Build with the
repo root as context, like every other Dockerfile here, and drop the unused
`docker-push-ui`.

## Acceptance criteria (from the issue)

- `make docker-build-ui`, from `src/` and from the repo root, builds the UI
  image with the repo root as context (`docker build -f ui/Dockerfile ..`
  from `src/`), and the build succeeds.
- `docker-push-ui` is removed, with its root forward, unless something uses
  it (workflows, scripts and docs searched; the PR says what was found).
- The Makefile guard tests (`src/pkg/managementapi/makefile_guard_test.go`)
  and `AGENTS.md` stay accurate.
- `plans/backlog.md` item 16 struck with `(#369)`.

## What uses `docker-push-ui`

Searched `.github/`, `scripts/`, `infrastructure/`, `docs/` and every
markdown file for `docker-push-ui` and `vrsky/ui`:

- `docker-push-ui`: only the two Makefiles and two old plans
  (`issue-361`, `issue-367`). No workflow, script or doc runs it.
- `vrsky/ui`: production builds it in ACR (`build-push.yml` and
  `infrastructure/azure/build-push-acr.sh`, both `--file ui/Dockerfile .`);
  the Kubernetes manifest and DEPLOYMENT_GUIDE name the GHCR image. None of
  them push to Docker Hub `vrsky/ui`.

So `docker-push-ui` goes.

## Files

- `src/Makefile`: fix `docker-build-ui`, remove `docker-push-ui` (rule and
  `.PHONY`).
- `Makefile`: remove the `docker-push-ui` forward (rule and `.PHONY`).
- `src/pkg/managementapi/makefile_guard_test.go`: a guard that the
  `docker build` commands `make -n` prints use a context that holds every
  file the Dockerfile copies.
- `plans/backlog.md`: strike item 16; add item 19 for the three
  `docker build ... ui/` lines in DEPLOYMENT_GUIDE's troubleshooting, which
  have the same wrong context (out of scope here).
- `AGENTS.md`: checked; it doesn't name either target, and "the root
  forwards every one of them" stays true. No change expected.

## Approach

- Recipe becomes `docker build -f ../ui/Dockerfile -t vrsky/ui:latest ..`,
  run from `src/` (the root forward runs it there too via `make -C src`).
  The root `.dockerignore` already allowlists `ui/` minus `node_modules`
  and `dist`, which is what `az acr build` uses.
- The guard test runs `make -n docker-build-ui` in `src/`, reads the
  `docker build` line, resolves `-f` and the context against `src/`, and
  checks each `COPY` source in the Dockerfile (skipping `--from`) matches
  something under the context. That is the check the build failed on, and
  it runs in CI without Docker. Skips when `make` isn't on PATH, like the
  existing help test.

## Risks

- None for production: ACR builds don't go through the Makefile.
- `ui/.dockerignore` stops applying (the root one does instead). It only
  matters for a `ui/`-context build, which this removes.

## Verification

- `make docker-build-ui` from `src/` and from the root: a real local build,
  no push; then `docker rmi vrsky/ui:latest`.
- The new guard fails against the old recipe (mutation) and passes now.
- `cd src && go test -count=1 ./pkg/managementapi/ -run Makefile`,
  `go vet ./pkg/managementapi/`, `golangci-lint run ./pkg/managementapi/...`,
  `gofmt -l`.
- Root `make help` lists `docker-build-ui` and not `docker-push-ui`.
- An independent checker subagent confirms the build and the guard tests
  against the acceptance criteria.
