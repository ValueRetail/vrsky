# Refresh the session without resetting the page (#630)

Backlog item 124. Auth store change, so the PR carries `agent:needs-human`.

## Goal

After a refused workspace delete (403 or 404) `TenantSelector` calls
`checkAuth()`. That sets `isLoading: true`, `ProtectedRoute` answers with the
spinner, and the builder remounts: open panels, event history and selection
are gone (the canvas survives in `canvasStore`). A refresh for a user who is
already on a page should leave `isLoading` alone.

## Acceptance criteria

1. A refused delete refreshes through `/me` without `isLoading` ever going
   true, and a child of `ProtectedRoute` is not remounted (mount count and
   local state survive).
2. A background refresh that gets a 401 or 403 (`getMe` drops the token)
   still signs out. A transient failure (network, 5xx) keeps the user and
   shows the "Could not refresh your account" toast, as #611 does.
3. The initial load (`App`, `ProtectedRoute` mount, the Retry button) and
   `login` still go through `checkAuth` with the spinner.
4. The new tests fail without the fix.
5. Backlog item 124 is struck with #630. Item 125 (inactive workspace
   fallback) is untouched.

## Files

- `ui/src/store/authStore.ts`: `checkAuth({ background })` and
  `refreshAuth()`.
- `ui/src/components/Tenants/TenantSelector.tsx`,
  `ui/src/components/Tenants/CreateTenantModal.tsx`: callers.
- Tests: `ui/src/store/authStore.test.ts`,
  `ui/src/components/Tenants/TenantSelector.test.tsx`,
  `ui/src/components/Tenants/CreateTenantModal.test.tsx`.
- `plans/backlog.md`.

## Approach

`checkAuth` takes an optional `{ background: true }`. In that mode it skips
every `isLoading` write; the rest of its logic (no token, success, #611
transient failure, sign-out when the token is gone) is shared, so the two
paths cannot drift. `refreshAuth()` is the call site API: it runs
`checkAuth({ background: true })` when there is a user in the store and a
plain `checkAuth()` otherwise, so a caller that lost its user in the
meantime still gets the normal load.

Callers of `checkAuth`:

| Caller | Change | Why |
|---|---|---|
| `App` mount, `ProtectedRoute` mount | none | initial load, the spinner is wanted |
| `ProtectedRoute` Retry | none | no user yet |
| `login` | none | login page, not under `ProtectedRoute`; sets `isLoading` itself |
| `TenantSelector`, delete refused (403/404) | `refreshAuth` | the bug; the workspace on screen usually still exists |
| `TenantSelector`, delete succeeded | none | the current workspace is gone, so a reset page is correct |
| `CreateTenantModal` on complete | `refreshAuth` | same remount for no reason; the user stays in their workspace |

## Risks

- A background refresh that returns a different `currentTenant` (the
  selected one is gone) does not remount the page, same as `switchTenant`.
  Not new.
- A refresh finishing after `expireSession` can put the user back. Same as
  `checkAuth` today.

## Verification

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`
in `ui/`. The new tests were run against the old `TenantSelector` call
(`checkAuth` in place of `refreshAuth`) and fail there.
