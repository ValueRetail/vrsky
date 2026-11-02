# checkAuth picks only an active workspace (#637)

Closes backlog item 125. Auth state, so the PR carries `agent:needs-human`.

## Problem

When the saved workspace is gone, `checkAuth` in `ui/src/store/authStore.ts`
fell back to `meData.current_tenant`, then `tenants[0]`, whatever their
status. `switchTenant` and the `TenantSelector` options refuse anything that
is not `active`, so the UI could end up on a workspace the user could not
have chosen.

## What the server sends

`GetMe` (`src/pkg/managementapi/auth_handler.go`) sets `current_tenant` to
`tenants[0]`. `GetUserTenants` (`repo_tenant.go`) orders by `created_at` and
does not filter on status, so the first one can be `provisioning`, `failed`
or `terminating`. Sign-up creates its workspace as `active`
(`repo_tenant.go:43`); a workspace made later through the create modal is
`provisioning` until it is set up. The server is not changed.

## Fix

`ui/src/store/authStore.ts`, in `checkAuth` (and so `refreshAuth`, which
calls it):

1. the saved workspace, if it is in the list and active (as before);
2. else `current_tenant`, if it is active;
3. else the first active workspace;
4. else none: `currentTenant` is `null`.

For 4 the user stays signed in with `tenants` filled in, so the selector
still lists their workspaces (inactive ones greyed out). `null` is already
the state of a user without workspaces and the pages guard it (`ApiKeyPage`,
`UsersPage`, `PropertyEditor`, the `RootLayout` effect). The saved id in
localStorage is left alone, so a workspace that finishes provisioning is
picked again at the next refresh. `setActiveTenantId` is not called when
nothing is selected, as before for an empty list.

## Review follow-up: no stale tenant id, and a placeholder

With nothing selected, the API client's active tenant id would stay at the
build-time default or the previous workspace's id, and loaders that do not
check `currentTenant` would send it as `X-Tenant-ID`. So:

- `checkAuth` calls `setActiveTenantId('')` when no workspace is selected.
- The request interceptor (`services/api.ts`), `fileApiHeaders`
  (`PipelineBuilder.tsx`) and `workerEvents.ts` leave the header out when the
  id is empty. The server's `TenantIDMiddleware` answers a missing header
  with 400 "Missing or invalid tenant ID header", which is the honest answer.
- `TenantSelector` shows a disabled "No active workspace" option when
  `currentTenant` is null, so the browser does not show the first (disabled)
  workspace as if it were selected.

## Not changed

The #611 sign-out and `loadFailed` paths and the #630 `refreshAuth` rule
(no `isLoading` for a signed-in user) are untouched.

## Tests (`ui/src/store/authStore.test.ts`)

Run for both `checkAuth` (spinner path) and `refreshAuth`:

- saved workspace gone, first one inactive: the first active is selected
- no saved workspace, first one inactive: same
- `current_tenant` active: used
- saved workspace gone, none active: `currentTenant` null, still signed in,
  saved id kept
- saved workspace still active (not the first): kept
- saved workspace now inactive: not kept

Plus `refreshAuth` with the current workspace deleted: moves to an active
one with `isLoading` never true. Nine of these fail on the old code.

## Done when

`npx tsc -b`, `npm run lint`, `npm run build` and
`npx vitest run --coverage` pass in `ui/`, CI is green, and backlog item 125
is struck with #637.
