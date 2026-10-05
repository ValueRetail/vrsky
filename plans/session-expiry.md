# Expired sessions: send the user to login instead of showing feature errors

## Open questions

None.

## Goal

When a session has expired (24 h, `DefaultSessionDuration`) in a tab that is
still open, the next thing the user does takes them to the login page with
"Your session has expired — log in again", and after logging in they land
back where they were. Today nothing notices: every page shows its own
unrelated error. Seen in prod on 2026-10-05: Settings → API Key showed "No
API key generated yet" and "Failed to rotate API key" for a workspace that
simply needed a new login.

## What is wrong (checked in the code)

- Login stores the session token in `sessionStorage` (`vrsky_session_token`)
  and the server also sets the `vrsky_session` cookie. Both die after 24 h.
- `ui/src/services/api.ts` (shared axios client): the response interceptor
  turns every non-2xx into a `VRSkyAPIError`; a 401 gets no special handling.
- `ProtectedRoute` only checks the session when it mounts (`checkAuth`), so a
  tab that stays on one page never re-checks.
- `ui/src/services/tenantDataService.ts` (API key, connection requests, data
  connections, access log) bypasses the shared client with its own `fetch`
  helper and always sends `Authorization: Bearer ${token}` — `Bearer null`
  when there is no token.
- `CreateTenantModal.tsx` does the same with a raw `fetch`.
- `ApiKeyPage.tsx` treats **any** failure of the key lookup as "no key yet"
  (`.catch(() => setApiKey(null))`), which is what made an expired session
  look like a missing key.
- `PipelineBuilder.tsx` (file endpoints) and `workerEvents.ts` (SSE) use raw
  `fetch` for good reasons (multipart, streaming); they only add the header
  when a token exists, but a 401 there is just another error.

## Approach

### One place decides "the session is gone"

New `ui/src/services/sessionExpiry.ts`:

```ts
setSessionExpiredHandler(fn)   // registered once by the auth store
reportUnauthorized()           // called by anything that got a 401
```

`reportUnauthorized()` does not log the user out on the 401 alone — a 401
can also mean "this particular thing refused you". It asks the server:
`authService.getMe()`. Only if that fails too is the handler called. One
check at a time (concurrent 401s share it), and it is a no-op when there is
no session to lose.

`authStore.sessionExpired()` (the handler): clear the token, reset the user
state, set `sessionExpired: true`. `ProtectedRoute` already redirects to
`/login` with `state.from` when `isAuthenticated` turns false, so no
navigation code is needed. `LoginPage` shows the expiry notice while the
flag is set; a successful login clears it and returns to `from`.

The module exists so `api.ts` does not import the store (the store imports
`api.ts`).

### Who reports

- `api.ts` response interceptor: on status 401 → `reportUnauthorized()`,
  then reject as today.
- `tenantDataService.ts` moves onto the shared client (same function
  signatures; errors become `VRSkyAPIError` with the server's message, which
  the pages already show or ignore). No more `Bearer null`.
- `CreateTenantModal.tsx` → shared client.
- `PipelineBuilder.tsx` file requests and `workerEvents.ts`: keep `fetch`,
  call `reportUnauthorized()` on a 401.
- `authService`'s own client is left alone: a 401 on login is a wrong
  password, and `getMe` is what the check itself uses.

### API Key page

Only a 404 means "no key yet". Anything else shows an error notification
with the server's message instead of pretending there is no key.

## Files

| File | Change |
|---|---|
| `ui/src/services/sessionExpiry.ts` (new, + test) | handler registry, verified 401 check |
| `ui/src/services/api.ts` | interceptor reports 401 |
| `ui/src/store/authStore.ts` | `sessionExpired` flag + action, registers the handler, login clears the flag |
| `ui/src/pages/LoginPage.tsx` (+ test) | expiry notice |
| `ui/src/services/tenantDataService.ts` (+ test) | shared client |
| `ui/src/components/Tenants/CreateTenantModal.tsx` | shared client |
| `ui/src/pages/ApiKeyPage.tsx` (+ test) | 404 vs other errors |
| `ui/src/pages/PipelineBuilder.tsx`, `ui/src/services/workerEvents.ts` | report 401 |

No backend change.

## Tests (vitest)

- `sessionExpiry.test.ts`: 401 + `getMe` fails → handler called **once** for
  three concurrent 401s; 401 + `getMe` succeeds → not called; no token → not
  called. *Mutation: skip the `getMe` check → the "401 but session fine"
  test fails; drop the single-flight → the "once" test fails.*
- `api` interceptor: a 401 response calls `reportUnauthorized`; a 403/500
  does not.
- `authStore`: `sessionExpired()` clears the token and user and sets the
  flag; a successful `login` clears it.
- `LoginPage`: shows the notice when the flag is set.
- `tenantDataService`: requests go through the shared client — captured via
  an axios adapter: correct URL/method/body, **no `Authorization` header
  when there is no token**, and the bearer when there is one.
- `ApiKeyPage`: 404 → "No API key generated yet"; a 500 → error
  notification, not the empty state.
- `npx tsc --noEmit`, `npx eslint`, `npm run test:coverage` (gate
  80/75/80/80), `npm run build`.

## Verify in the app

Dev stack: log in, delete the session row (or wait it out with a shortened
duration), click around → redirected to `/login` with the notice → log in →
back on the same page. Prod after deploy: nothing to do until a session
expires; the API Key page then behaves like every other page.

## Risks

- **Logging someone out by mistake**: prevented by the `getMe` confirmation;
  a 401 from anything else leaves the session alone.
- `tenantDataService` on the shared client now also sends `X-Tenant-ID`;
  those routes ignore it (`TenantIDMiddleware` exempts `/api/v1/tenants`).
- One extra `GET /auth/me` per burst of 401s.

## Non-goals

Refreshing or extending sessions; changing the 24 h lifetime; moving the
token out of `sessionStorage`.
