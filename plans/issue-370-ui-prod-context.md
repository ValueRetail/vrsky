# ui-prod in docker-compose.ui.yml has a broken build context (#370)

No open questions.

## Goal

`docker compose -f docker-compose.ui.yml build ui-prod` fails at the first
`COPY ui/...` because the service builds with `context: ./ui`, while
`ui/Dockerfile` expects the repo root as context (like every Dockerfile
here). Make it build.

## Acceptance criteria (from the issue)

- `ui-prod` builds with the repo root as context and
  `dockerfile: ui/Dockerfile`, the way the `ui` service in the same file
  does; `docker compose -f docker-compose.ui.yml build ui-prod` succeeds.
- Only `docker-compose.ui.yml` changes (plus this plan and the backlog line).
  `docs/DEPLOYMENT_GUIDE.md` is being rewritten under #371; if its "Option 2"
  text needs a change after this fix, the PR says so instead of editing it.
- `plans/backlog.md` item 17 struck with `(#370)`.

## Files

- `docker-compose.ui.yml`: the `ui-prod` build block only.
- `plans/backlog.md`: strike item 17's title, append `(#370)`.
- `plans/issue-370-ui-prod-context.md` (this file).

## Approach

- `ui-prod.build`: `context: .`, `dockerfile: ui/Dockerfile`, keep
  `target: nginx`. Same shape as the `ui` service.
- With the root as context, the root `.dockerignore` applies (it keeps
  `ui/` and drops `ui/node_modules` and `ui/dist`), so the upload stays small.
  `ui/.dockerignore` stops being used by this service; that's already true
  for the `ui` service and for `docker build -f ui/Dockerfile .`.
- Check the rest of the file for the same problem. The `ui` service already
  uses `context: .`; there are no other build blocks. Other oddities (the
  obsolete `version:` key, `VITE_*` set as runtime env on an nginx image where
  they do nothing) are out of scope and get noted in the PR, not fixed.

## Risks

- None for production: this compose file isn't used by CI or the ACR build
  (`build-push.yml` and `build-push-acr.sh` call `az acr build -f
  ui/Dockerfile .` directly). `ui/Dockerfile` is unchanged.

## Verification

- Before the fix: the build fails with `"/ui/nginx.default.conf": not found`
  (reproduced).
- After: `docker compose -f docker-compose.ui.yml build ui-prod` exits 0;
  `docker compose -f docker-compose.ui.yml config` shows the resolved context
  as the repo root. Remove the built image afterwards. No push.
- An independent checker subagent confirms from the acceptance criteria.
- `git diff --stat origin/main` lists only the three files above.
