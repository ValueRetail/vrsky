# Issue #399: the dev UI container can't reach the API

Backlog item 39, found in #392.

## Problem

`ui/src/services/api.ts` sends relative `/api/...` requests and Vite's dev
proxy in `ui/vite.config.ts` forwards them to a hardcoded
`http://localhost:3000`. In the `ui` service of `docker-compose.ui.yml` that
address is the UI container itself, so every API call fails. The service also
sits on its own `vrsky` bridge network, so it can't see `management-api` by
name, and the API is published on `127.0.0.1` only, so `host.docker.internal`
doesn't help.

## Acceptance criteria

- `vite.config.ts` reads the `/api` proxy target from `API_PROXY_TARGET`
  and falls back to `http://localhost:3000`, so `npm run dev` on the host
  behaves as today.
- The compose `ui` service joins the core stack's external `vrsky-network`
  and sets `API_PROXY_TARGET=http://management-api:3000` (service
  `management-api` in `docker-compose.yml`, `MGMT_API_LISTEN: ":3000"`).
- With the core stack up, following "Option 1" in `docs/DEPLOYMENT_GUIDE.md`,
  a request to `/api/...` on the dev server reaches the API (curl evidence in
  the PR).
- A guard test in `src/pkg/managementapi/compose_ui_guard_test.go` fails if
  the compose target stops pointing at a real service and port on
  `vrsky-network`, or if `vite.config.ts` stops reading the variable.
- UI checks pass (`npx tsc -b`, lint, `npm run build`,
  `npx vitest run --coverage`), and `go test -count=1 ./pkg/managementapi/...`.
- Backlog item 39 struck with "(#399)".

## The env var name

`API_PROXY_TARGET`, without a `VITE_` prefix:

- Vite exposes every `VITE_*` variable to client code through
  `import.meta.env`. The proxy target is only used by the dev server in
  Node; the browser never needs it, so it shouldn't be in that namespace.
- `TestComposeUIEnvIsRead` only checks `VITE_*` names against `env.ts`, so a
  non-`VITE_` name passes it untouched. No change to that guard is needed.
- `vite.config.ts` reads it through `process.env`, which is what compose
  sets. `loadEnv` would also read `ui/.env*` files; nothing needs that today.

## `/ws`

Vite doesn't proxy `/ws`, so there's nothing to change there.
`config.wsUrl` (`VITE_WS_URL`, default `http://localhost:3000`) is a URL the
browser opens, not the container. The browser runs on the host, where the
API is published on `127.0.0.1:3000`, so that default works with the dev
container too. Left alone.

## Files

- `ui/vite.config.ts`: proxy target from `process.env.API_PROXY_TARGET`.
- `docker-compose.ui.yml`: `ui` sets `API_PROXY_TARGET`, joins external
  `vrsky-network`. `ui-prod` keeps its own `vrsky` network (it serves the
  built bundle and has no proxy to fix; out of scope).
- `src/pkg/managementapi/compose_ui_guard_test.go`: new guard plus
  table-driven cases for its checker.
- `docs/DEPLOYMENT_GUIDE.md` ("Option 1") and `docs/operator/install.md`:
  the core stack must be up first; the variable for `npm run dev` against a
  different API.
- `plans/backlog.md`: strike item 39.

## Risks

- `docker compose -f docker-compose.ui.yml --profile dev up ui` now fails
  when `vrsky-network` doesn't exist ("network vrsky-network declared as
  external, but could not be found"). That is the right failure: without the
  core stack there's no API to reach. The docs say to start the stack first.
- Compose's network check applies only to the services being started, so
  `--profile prod up ui-prod` doesn't need the core stack.
- No production impact: `vite.config.ts`'s `server` block only affects
  `vite dev`; the production image serves `dist/` from nginx.

## Verification

- `npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`
  in `ui/`.
- `go test -count=1 ./pkg/managementapi/...` from `src/`, plus `go vet` on the
  package.
- `docker compose -f docker-compose.ui.yml --profile dev config -q`.
- Live: if the core stack is running, start the dev `ui` as a throwaway
  project (`-p vrsky-issue399 --profile dev`), curl
  `http://localhost:5173/api/v1/auth/me` and compare with the same request
  straight to `127.0.0.1:3000` (both should be the API's 401 JSON), then
  `down` the throwaway project without `-v`. The API's health endpoints
  (`/healthz`, `/readyz`) are not under `/api`, so Vite doesn't proxy them.
