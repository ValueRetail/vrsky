# Docs still name make targets that don't exist (#367)

No open questions.

## Goal

`docs/DEPLOYMENT_GUIDE.md` and `PROJECT_STRUCTURE.md` tell readers to run
make targets for building and pushing images that no longer exist. Point
them at what builds images now, and make sure no other doc names a dead
target.

## Acceptance criteria (from the issue)

- Every make command and target named in `docs/` and `PROJECT_STRUCTURE.md`
  exists. All of them checked, not only the lines the issue lists.
- Where a removed target stood for a real task (building or pushing images),
  the doc says how it's done now: in ACR by `build-push.yml` and
  `infrastructure/azure/build-push-acr.sh`, locally by `docker compose
  build`. Checked against those files.
- `PROJECT_STRUCTURE.md` corrected, or replaced with a pointer to
  `AGENTS.md`; the PR says which.
- `mkdocs build --strict` passes.
- `plans/backlog.md` item 15 struck with `(#367)`.

## Audit

`make <target>` in `docs/**`, `PROJECT_STRUCTURE.md` and the top-level
markdown, each run with `make -n` from the directory the doc implies:

| Target | Where | Exists |
|---|---|---|
| `build`, `build-agent`, `help`, `lint`, `lint-tenant`, `lint-connector`, `lint-openapi`, `test-db`, `test`, `fmt`, `clean` | AGENTS.md, CLAUDE.md, PROJECT_STRUCTURE.md, docs/sdk, docs/operator/remote-agent.md | yes (root and `src/`) |
| `up-core`, `up-core-remote`, `down-core` | README.md, AGENTS.md, docs/operator/{windows,remote-access,remote-agent}.md | yes (root) |
| `docker-build-ui`, `docker-push-ui` | docs/DEPLOYMENT_GUIDE.md | yes, but broken: `src/Makefile` passes `../ui` as the build context while `ui/Dockerfile` copies `ui/...` from the repo root, so the build fails at the first `COPY` (tried). `docker-push-ui` pushes `vrsky/ui:latest` to Docker Hub, which nothing uses. |
| `docker-build`, `docker-push`, `docker-build-consumer`, `docker-build-filter`, `docker-build-converter`, `docker-push-consumer`, `docker-push-filter`, `docker-push-converter` | docs/DEPLOYMENT_GUIDE.md, PROJECT_STRUCTURE.md | no |

## Files

- `docs/DEPLOYMENT_GUIDE.md`: the "Manual Docker Build Commands" and
  "Makefile Commands" sections only.
- `PROJECT_STRUCTURE.md`
- `plans/backlog.md` (strike item 15; add items 16-18 for the UI build
  context and the GHCR sections found along the way)

## Approach

- DEPLOYMENT_GUIDE: replace the two sections with one on building images
  by hand (`docker compose build <service>`, or `docker build -f
  <Dockerfile> .` from the repo root, since every Dockerfile uses the root as
  context) and one on production images (the deploy job in `build-push.yml`
  builds what `affected.sh` names with `az acr build`; by hand,
  `build-push-acr.sh [core|connectors|all]`, then `deploy-core-azure.sh`).
  Don't recommend the two UI targets, since they fail.
- PROJECT_STRUCTURE.md: almost all of it is wrong now (a root `go.mod`, a
  `src/docker-compose.yml`, `cmd/producer`, four packages, files that were
  deleted). `AGENTS.md` was rewritten from the code in #357 and has a
  layout table. Keeping two layout descriptions in sync is the reason this
  one rotted, so replace it with a short pointer to `AGENTS.md`.
- Leave the rest of DEPLOYMENT_GUIDE alone. Its GHCR sections are stale too
  but aren't about make; note them in the PR as a follow-up.

## Risks

- None at runtime; docs only. The mkdocs nav includes DEPLOYMENT_GUIDE, so a
  broken link would fail `--strict`.

## Verification

- `make -n` for every target the changed files name, from the stated
  directory.
- An independent checker subagent gets only the changed files and runs
  every command in the changed sections from the directory they state:
  dry runs for anything that builds images, pushes or starts services
  (`docker compose build --dry-run`, `bash -n` on the ACR script, no `az`
  calls against Azure).
- `mkdocs build --strict` in a scratch venv with `mkdocs-material`, as
  `docs.yml` installs.
