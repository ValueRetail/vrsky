# `checkAuth` signs the user out when `/me` fails for any reason (#611)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A failed `/me` that is not a rejected session (network error, 5xx) no
longer sends a user with a working session to the login page. Only a 401
from the server signs the user out.

## Acceptance criteria

1. `/me` answers 401: `checkAuth` signs the user out (as today).
2. `/me` fails for any other reason (network error, 5xx) while a user is
   already loaded: user, workspaces and current workspace stay, the user
   stays authenticated, and an error toast says the refresh failed.
3. The same failure at app load, with nothing cached: no redirect to the
   login page. `ProtectedRoute` shows "Could not reach the server" with a
   Retry button that runs `checkAuth` again.
4. A later successful `checkAuth` clears that state.
5. Tests for 1 to 4 fail on the old code (checked by reverting the fix).
6. Backlog item 116 struck with `(#611)`.

## What the server does

`GET /api/v1/auth/me` (`GetMe` in `src/pkg/managementapi/auth_handler.go`)
answers 401 for every invalid or expired session (`getAuthenticatedUser`
failing) and 200 otherwise. There is no other "your session is gone"
status, so 401 is the only sign-out signal.

## Files

- `ui/src/store/authStore.ts`: `checkAuth` and a new `loadFailed` flag.
- `ui/src/components/Auth/ProtectedRoute.tsx`: the retry state.
- `ui/src/store/authStore.test.ts`: new tests.
- `ui/src/components/Auth/ProtectedRoute.test.tsx`: new test file.
- `plans/backlog.md`: strike item 116.

## Approach

`getMe` clears the session token only on its own 401 and returns null for
every failure. `checkAuth` already runs after `hasSessionToken()` was true,
so when `getMe` returns null it asks `hasSessionToken()` again, the way
`reportUnauthorized` in `services/sessionExpiry.ts` does:

- token gone: the server rejected the session, sign out (unchanged).
- token still there: transient. With a loaded user, keep the state, set
  `isLoading: false`, show an error toast. With no user, set
  `loadFailed: true` (and `isInitialized: true`).

No change to `getMe`, so `sessionExpiry` and its tests are untouched. The
`catch` branch (getMe does not throw today) is treated as transient too,
rather than signing out on an unexpected exception.

## Decisions

- Retry screen at app load instead of the login page: the session is
  probably fine, and the login page would also be wrong for an SSO user
  whose token is still valid.
- Toast, not a banner, for the signed-in case: the toast mechanism exists
  and the page keeps working.
- `getMe` stays as is; the token check is the signal, same as
  `reportUnauthorized`.
- Backlog items 124 (spinner remount) and 125 (inactive workspace
  fallback) are not touched. `isLoading` is still set around the call.

## Risks

- A permanently failing `/me` with a token the server would reject but
  can't answer for (e.g. 502 forever) leaves the user on the retry screen
  at load. Retry and reload are both available; the alternative is the
  bug.
- `loadFailed` must be cleared on every other outcome, or the retry screen
  could stick after a logout. `logout`, `expireSession` and the success
  path reset it.

## Verification

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`
in `ui/`. New tests run against the reverted fix and must fail.
