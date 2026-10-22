# Stop tracking installed npm dependencies

Checked in the repo on 2026-10-22.

## Open questions

1. **The root `node_modules/` is tracked too** — 530 files, Playwright,
   added in `bd1ec08a` ("UI fixes", 2026-03-03) together with a root
   `package.json` whose only content is `devDependencies: { playwright }`.
   Nothing in the repo uses it: no script, workflow or test imports
   Playwright outside that `package.json` (`e2e-nightly.yml` runs Go only).
   Recommended: **untrack root `node_modules/` in the same PR** (same bug,
   same fix). Separately: delete the root `package.json` +
   `package-lock.json` too, or leave them? Recommended: **leave them** — out
   of scope; a one-line follow-up if you agree they're dead.
2. **Any other tracked build output?** Checked: no `dist/`, `build/`,
   `coverage/` or `*.tsbuildinfo` is tracked. One unrelated find:
   `infrastructure/scripts/bin/helm`, a 57 MB Linux binary committed in
   `eb54cb06` despite `bin/` being ignored. Recommended: **not in this PR**;
   I'll propose it as a follow-up (it may be used by the k3d scripts).
3. **Does CI or a Dockerfile rely on the committed files?** No:
   - `ui/Dockerfile` copies only `ui/package*.json` + config, then `npm ci`.
   - Root `.dockerignore` and `ui/.dockerignore` both exclude `node_modules`.
   - `build-push.yml` runs `npm ci` in `ui/` before `test:coverage` and
     `build`; `vuln-scan.yml` runs `npm audit` from the lockfile only.
   - `docker-compose.ui.yml` mounts an anonymous volume over `/app/node_modules`.

**Decided 2026-10-22:** untrack root `node_modules/` here; then, on
Ludvik's request, also in this PR: delete root `package.json` +
`package-lock.json`, and untrack `infrastructure/scripts/bin/helm`
(`local-up.sh` runs from `infrastructure/` per its README, so it checks
`infrastructure/bin/helm`, never `scripts/bin/helm`, and never invokes helm
after that check; the binary is linux-amd64 only). The file stays on disk
and is now covered by the existing `**/bin/` ignore.

## Goal

`ui/node_modules` (7,302 files, ~227 packages, no `typescript/bin` link in
`.bin`) and root `node_modules` stop being tracked, so a fresh worktree has
no half install, `npm ci` dirties nothing, and `npm run build` works after it.

## Affected files

- `ui/node_modules/**` — removed from the index only (`git rm -r --cached`).
- `node_modules/**` — same, if question 1 is approved.
- `.gitignore` (root) — add a `node_modules/` rule.
- `ui/.gitignore` — unchanged; it already has `node_modules`
  (Vite template). That's why `git check-ignore` matches `ui/node_modules`
  already: the files were committed before or around the ignore rule, and
  tracked files ignore `.gitignore`.

## Approach

1. `git rm -r --cached ui/node_modules node_modules` (files stay on disk in
   existing checkouts; other worktrees/clones lose them on the next pull,
   which is fine — `npm ci` restores them).
2. Root `.gitignore`: add under "Dependency directories", next to `vendor/`:
   `node_modules/`. Root and `ui/` rules then agree; no `!` negation
   anywhere re-includes `node_modules` (checked).
3. Verify (below). Diff shown to you, uncommitted.

## Risks

- **Large deletion diff** (~7,800 files). Review with
  `git diff --cached --stat | tail -1` and
  `git diff --cached --name-only | grep -v node_modules/` (should be just
  `.gitignore`). Repo history still holds the blobs; shrinking history is a
  rewrite and out of scope.
- **Existing checkouts after pulling**: git deletes the formerly-tracked
  files from their working tree → run `cd ui && npm ci` once. Your main
  checkout at `~/Value-Retail/vrsky` included.
- Nothing builds from the committed files (question 3), so CI and images
  are unaffected.

## How we verify

1. From a fresh worktree off this branch's index state (`git worktree add`
   in the scratchpad after staging, or `git stash`-free: check out the
   staged tree into a temp dir):
   `cd ui && npm ci && npm run build && npm run lint` — all pass.
2. `git status --porcelain` after `npm ci` is empty.
3. `docker build -f ui/Dockerfile .` from the repo root succeeds (if Docker
   is running locally; otherwise reasoned from the Dockerfile, as above).
4. `npm run test:coverage` in `ui/` — same step CI runs — passes.
5. `git check-ignore -v node_modules/x ui/node_modules/x` → both ignored.
