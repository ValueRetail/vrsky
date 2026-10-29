# A workspace deleted elsewhere stays in the selector (#552)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Delete a workspace in one tab (or have another owner delete it) and it stays
in the selector of every other open session. Deleting it again there gets a
403 ("you are not a member of this tenant": the membership rows went with
the workspace) or a 404 (the workspace row is gone), and since #514
`TenantSelector` only shows that error. The workspace stays listed and
selected, and every later request for it fails the same way.

A 403 or 404 is an answer from the server: the session works and the
network is up. Refreshing through `checkAuth` there drops the stale
workspace, and it does not bring back the problem #514 avoided, where a
network error on the delete turned into a logout through a failed `/me`.

## Acceptance criteria

1. A delete that the server answers with 403 or 404 shows the same error
   notification as before (title "Workspace not deleted", the API's
   message), then refreshes through `checkAuth` (one `GET /api/v1/auth/me`).
   A workspace that `/me` no longer lists leaves the selector, and the
   selection moves to one it does list.
2. Any other failure (a network error with no status, a 409, a 5xx, an
   error that is not a `VRSkyAPIError`) behaves as in #514: the
   notification, no `/me` request, the session and the selector unchanged.
3. A 403 for a workspace that still exists (the user is a member but not
   the owner) refreshes too, and the workspace stays because `/me` still
   lists it. Nothing else changes for the user except the toast.
4. A successful delete still refreshes through `checkAuth` and shows no
   error.
5. Tests over the real component (only HTTP mocked) cover 1 to 3 and fail
   without the fix. Mutation run: drop the 403 branch, drop the 404
   branch, refresh on every error, and never refresh on error; a test
   fails each time.
6. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
7. Backlog item 103 struck with `(#552)`.

## Files

- `ui/src/components/Tenants/TenantSelector.tsx`
- `ui/src/components/Tenants/TenantSelector.test.tsx`
- `plans/backlog.md`

## Decisions

- Read the status the way `ApiKeyPage` does:
  `err instanceof VRSkyAPIError ? err.details?.status : undefined`. The API
  client's interceptor puts the HTTP status there for every answered
  request and leaves it out for network errors, so "has a status of 403 or
  404" means "the server answered and said no".
- Refresh on 403 as well as 404. The server answers 403 both for a
  workspace that is gone (no membership row left) and for a non-owner. In
  the second case the refresh is a wasted `/me` call that changes nothing;
  telling the two apart would mean matching on the error text.
- Use `checkAuth`, as the backlog item proposes, not a new store action.
  `/me` can still fail right after the delete's answer and log the user
  out, the same as at any other `checkAuth` call. That is a property of
  `checkAuth` itself, not of this path, and the window is one request.
- Show the error first, then refresh. The user tried to delete something
  and it did not happen through their click; the toast says why, and the
  selector catching up is a side effect.
- No other status triggers a refresh. A 401 already goes through the
  session-expiry handler in the API client; a 5xx or 409 says nothing
  about whether the workspace still exists.
