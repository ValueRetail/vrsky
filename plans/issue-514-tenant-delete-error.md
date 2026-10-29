# A failed workspace delete says nothing (#514)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

`TenantSelector`'s delete swallows every error behind a comment that says
"checkAuth will refresh state regardless", but nothing calls checkAuth on
that path. The user confirms a destructive action and then sees nothing
happen: no message, the workspace still selected. Tell them it failed and
why, and make the comment match what the code does.

## Acceptance criteria

1. A delete the API rejects shows one error notification that carries the
   API's message (a fallback text when the error has none).
2. After a failed delete the workspaces, the current workspace and the
   session stay as they were; `/me` is not called, so a network error never
   signs the user out.
3. A delete that succeeds still refreshes through `checkAuth` and shows no
   error notification.
4. Tests over the real component (only the HTTP client mocked) cover 1 to 3
   and fail without the fix. Mutation run: drop the notification, or move
   `checkAuth` into the catch, and they fail.
5. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
6. Backlog item 73 struck with `(#514)`.

## Files

- `ui/src/components/Tenants/TenantSelector.tsx`
- `ui/src/components/Tenants/TenantSelector.test.tsx`
- `plans/backlog.md`

## Decisions

- Fix the code and the comment, without calling `checkAuth` on failure.
  `checkAuth` treats any `/me` failure as "signed out" and clears the
  session state. On the usual causes of a failed delete (a network error,
  a 5xx, a 403 for a non-owner) the workspace still exists, so a refresh
  gains nothing and, on a network error, would log the user out.
- Show the API's message through `apiErrorMessage`, the helper the
  password pages already use, under the title "Workspace not deleted".
- The checkAuth after a successful delete moves out of the `try`, so the
  catch only ever sees the delete's own error. `checkAuth` never throws.
