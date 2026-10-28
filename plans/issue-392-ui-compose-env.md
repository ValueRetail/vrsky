# docker-compose.ui.yml sets env vars the UI never reads (#392)

No open questions.

## Goal

`docker-compose.ui.yml` sets `VITE_API_BASE_URL` and `VITE_ENVIRONMENT` on
the `ui` dev service and on `ui-prod`. The UI reads neither
(`ui/src/config/env.ts` reads `VITE_API_URL`, `VITE_WS_URL`,
`VITE_TENANT_ID`, `VITE_LOG_LEVEL`, `VITE_WEBHOOK_INGRESS_URL`,
`VITE_DOCS_URL`), so the keys do nothing and suggest the API URL can be set
there. The file also has an obsolete top-level `version:` that makes
compose warn. Backlog item 31, found in #377.

## Acceptance criteria (from the issue)

- Both keys gone from both services; `version:` gone.
- If a service needs a variable the UI really reads, set that one instead,
  under the name `env.ts` uses.
- `docker compose -f docker-compose.ui.yml --profile dev config` and
  `--profile prod config` are clean, with no obsolete-version warning.
- The `ui-prod` guard test from #379 still passes.
- Docs that mention the two names are corrected.
- `infrastructure/kubernetes/ui/configmap.yaml` untouched; the PR says so.
- Backlog item 31 struck with `(#392)`.

## Does either service need a real variable?

No.

- `ui-prod` serves a bundle built at `docker build` time. Vite freezes
  `VITE_*` into it then, so runtime environment on the nginx container
  can't change anything. Nothing to set.
- `ui` (dev) runs `npm run dev` in the container. The axios client
  (`ui/src/services/api.ts`) always uses `baseURL: ''`, so API calls are
  relative `/api/...` and go through Vite's proxy in `ui/vite.config.ts`,
  whose target is a hardcoded `http://localhost:3000`. Inside the container
  that's the container itself, so the proxy can't reach a management API
  whatever env is set. `VITE_API_URL` only feeds a few direct URLs (SSE,
  SSO, file manager), and pointing it at the host would make those
  cross-origin while the axios calls still go through the dead proxy. So
  setting it would not make the service work and would split the UI's
  traffic two ways. Left unset; the proxy target goes to the backlog as a
  new item (needs `vite.config.ts` to take the target from env, and the
  service on the stack's external `vrsky-network` with the target
  `http://management-api:3000`; the host port is bound to 127.0.0.1 only,
  so `host.docker.internal` can't reach it).

## Files

- `docker-compose.ui.yml`: drop `version:` and both `environment:` blocks.
- `src/pkg/managementapi/compose_ui_guard_test.go`: a guard that `ui` sets
  only `VITE_*` names `env.ts` reads and `ui-prod` sets none (a runtime
  `VITE_*` can't reach a built bundle).
- `plans/backlog.md`: strike item 31 with `(#392)`; add the dev proxy item
  after the highest number on main (38 today; other open PRs may add
  numbers too).
- This plan.

No docs outside `plans/` mention the two names except
`infrastructure/kubernetes/ui/configmap.yaml`, which is a manifest and stays
for a human-reviewed PR. It already carries a comment saying the keys are
dead.

## Risks

- Someone relying on `VITE_ENVIRONMENT` in the dev container: nothing reads
  it, so nothing changes.
- The guard parses `env.ts` with a regex on `getEnv('VITE_…'`. If `env.ts`
  stops using `getEnv`, the guard fails loudly (it requires at least one
  name) rather than passing silently.

## Verification

- `docker compose -f docker-compose.ui.yml --profile dev config -q` and
  `--profile prod config -q`: exit 0, no warning.
- From `src/`: `go test -count=1 ./pkg/managementapi/...`, `go vet`,
  `golangci-lint run ./pkg/managementapi/...`.
- Mutation: put `VITE_API_BASE_URL` back on `ui` and a `VITE_*` on
  `ui-prod`; the guard fails for each.
- With the base images cached: start `ui-prod` under project
  `vrsky-issue392`, `curl` `/` for 200, `down`, remove the throwaway image.
