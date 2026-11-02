# `checkAuth` signs the user out when `/me` fails for any reason (#611)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A failed `/me` that is not a rejected session (network error, 5xx) no
longer sends a user with a working session to the login page. Only a 401 or
403 from the server (a rejected session) signs the user out.

## Acceptance criteria

1. `/me` answers 401 or 403: `checkAuth` signs the user out (as today).
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
sits behind `SessionAuthMiddleware` (`session_middleware.go`), which
answers 401 for a missing, invalid or expired session (including any
`ValidateSession` error) and 403 when `user.CanLogin()` fails (suspended,
deleted, email not verified). Both mean the session is no good, so 401 and
403 are the sign-out signals. Everything else (network error, 5xx, 408,
429) is transient.

## Files

- `ui/src/services/authService.ts`: `getMe` drops the token on 403 too.
- `ui/src/store/authStore.ts`: `checkAuth`, a new `loadFailed` flag that `login`, `logout` and `expireSession` reset.
- `ui/src/components/Auth/ProtectedRoute.tsx`: the retry state, with Retry and "Sign in again".
- `ui/src/store/authStore.test.ts`: new tests.
- `ui/src/components/Auth/ProtectedRoute.test.tsx`: new test file.
- `plans/backlog.md`: strike item 116.

## Approach

`getMe` clears the session token only when the server rejects the session
(401, and 403 after review) and returns null for
every failure. `checkAuth` already runs after `hasSessionToken()` was true,
so when `getMe` returns null it asks `hasSessionToken()` again, the way
`reportUnauthorized` in `services/sessionExpiry.ts` does:

- token gone: the server rejected the session, sign out (unchanged).
- token still there: transient. With a loaded user, keep the state, set
  `isLoading: false`, show an error toast. With no user, set
  `loadFailed: true` (and `isInitialized: true`).

`getMe` changes only by also dropping the token on 403, so `sessionExpiry`
and its tests are untouched. The
`catch` branch (getMe does not throw today) is treated as transient too,
rather than signing out on an unexpected exception.

## Decisions

- Retry screen at app load instead of the login page: the session is
  probably fine, and the login page would also be wrong for an SSO user
  whose token is still valid.
- Toast, not a banner, for the signed-in case: the toast mechanism exists
  and the page keeps working.
- The token check is the signal, same as `reportUnauthorized`. `getMe`
  treats 401 and 403 as rejection rather than "any HTTP answer except
  5xx/408/429": the server uses exactly those two for a bad session, and a
  list of known statuses can't turn a new transient status (a proxy's 404,
  say) into a sign-out.
- The retry screen has a "Sign in again" button next to Retry that calls
  `logout`, so a persistent 502 is not a dead end.
- Backlog items 124 (spinner remount) and 125 (inactive workspace
  fallback) are not touched. `isLoading` is still set around the call.

## Risks

- A permanently failing `/me` (e.g. 502 forever) leaves the user on the
  retry screen at load. Retry, reload and "Sign in again" are all there;
  the alternative is the bug.
- `ValidateSession` errors, DB errors included, still become 401 on the
  server, so a database blip signs users out. Server side, out of scope
  here: backlog item 146.
- `loadFailed` must be cleared on every other outcome, or the retry screen
  could stick after a logout. `logout`, `expireSession` and the success
  path reset it.

## Verification

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`
in `ui/`. New tests run against the reverted fix and must fail.
