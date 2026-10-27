# Remove the unread ui/.dockerignore (#380)

No open questions.

## Goal

Every image builds with the repo root as context (`az acr build ... --file
ui/Dockerfile .`, `make docker-build-ui`, both services in
`docker-compose.ui.yml`), and Docker reads only the `.dockerignore` at the
root of the context. `ui/.dockerignore` is never read, but it looks like it
applies. Remove it and move whatever in it still matters into the root file.

## Acceptance criteria (from the issue)

- `ui/.dockerignore` is gone. What it excluded that matters for the UI
  build context is in the root `.dockerignore` if it wasn't covered already.
- `docker build -f ui/Dockerfile .` still works from the repo root, and the
  build context only shrinks or stays the same.
- No doc describes `ui/.dockerignore` as the file that applies.
- Backlog item 23 struck with `(#380)`.

## Files

- `ui/.dockerignore`: deleted.
- `.dockerignore`: new exclusions in the UI section.
- `docs/DEPLOYMENT_GUIDE.md`: "Build context" paragraph.
- `plans/backlog.md`: strike item 23; new item 32 (source maps, found here).
- `plans/issue-380-ui-dockerignore.md` (this file).

## Approach

The root file starts with `*`, re-includes `!ui`, then drops
`ui/node_modules` and `ui/dist`. Everything else under `ui/` goes into the
context. Pattern by pattern from `ui/.dockerignore`:

| ui/.dockerignore | Root file today | Decision |
|---|---|---|
| `node_modules`, `dist` | `ui/node_modules`, `ui/dist` | covered |
| `.git`, `.gitattributes`, `.github`, `.gitlab-ci.yml`, `.circleci`, `docs`, `CHANGELOG.md` | none exist under `ui/`; root ones dropped by `*` | nothing to add |
| `yarn.lock`, `build`, `.turbo` | not used (npm, Vite writes `dist`) | skip |
| `*.tsbuildinfo`, `.vite` | tsconfig puts build info in `node_modules/.tmp`; Vite caches in `node_modules/.vite` | covered by `ui/node_modules` |
| `coverage`, `.nyc_output`, `*.lcov` | let through | add (coverage is ~4 MB, 150 files after a local run) |
| `.env`, `.env.local`, `.env.*.local` | let through | add as `ui/.env*` (may hold secrets) |
| `*.log`, `npm-debug.log*`, `yarn-debug.log*`, `yarn-error.log*` | let through | add |
| `.eslintcache` | let through | add |
| `.DS_Store` | let through under `ui/` | add as `ui/**/.DS_Store` |
| `.vscode`, `.idea`, `*.swp`, `*.swo`, `*~`, `*.iml` | let through | skip: not present, harmless |
| `.gitignore`, `README.md` | let through | skip: small, harmless |

`ui/.env*` also drops the tracked `ui/.env.example`, which the old file kept.
Nothing copies it, and one pattern for all env files is harder to get wrong.

## Risks

- The root `.dockerignore` shapes every production image. If a new pattern
  matched something a Dockerfile COPYs, the ACR build on main would fail.
  Every COPY/ADD source across the Dockerfiles is `src/...`,
  `infrastructure/migrations` or a fixed list of `ui/` files and dirs; none
  matches the new patterns, and the before/after context diff below shows
  no tracked file under `ui/src` or `ui/tests` dropped.
- A local BuildKit build only transfers the files COPY asks for, so its
  "transferring context" size says little about what `az acr build`
  uploads (the whole filtered tree). The listing image measures that.

## Verification

- List the context before and after with a throwaway image
  (`FROM busybox`, `COPY . /ctx`, `find`), with a simulated dev checkout
  (the real `ui/coverage`, `.env*`, logs, `.DS_Store`, `.eslintcache`,
  `dist`, `node_modules`). Only removals, all under `ui/`.
- `docker build --no-cache -f ui/Dockerfile .` under a throwaway tag;
  check `index.html` and `assets/` in the image; remove the image. No push.
- `grep -rn 'ui/.dockerignore'` outside `plans/` finds nothing stale.
- CI green on the PR.
