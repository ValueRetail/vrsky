# Drop the unused websocket.ts and VITE_WS_URL (#428)

## Open questions

None.

## Goal

`ui/src/services/websocket.ts` is never imported. It opened an
`EventSource` on `${config.wsUrl}/api/v1/connections/{id}/metrics/stream`,
with `wsUrl` from `VITE_WS_URL` (default `http://localhost:3000`): another
origin in dev, so no session cookie, and the wrong host in production.
Nothing else reads `VITE_WS_URL`. Backlog item 49, found during #399.

The issue allows a second route, making the stream relative and using it.
Nothing in the UI wants live connection metrics today, so wiring it up
would be new feature work. Delete instead, and file the now-unused server
endpoint as a backlog item.

## Where the names appear (git grep on main, 2026-10-28)

| File | What |
|---|---|
| `ui/src/services/websocket.ts` | the module; no test file |
| `ui/src/config/env.ts` | `wsUrl: getEnv('VITE_WS_URL', ...)` |
| `ui/src/env.d.ts` | `VITE_WS_URL` in `ImportMetaEnv` |
| `ui/.env.example` | `VITE_WS_URL=http://localhost:3000` |
| `plans/sse-write-deadline.md` | lists websocket.ts as the stream's caller |
| `plans/backlog.md`, `plans/issue-392-*.md`, `plans/issue-399-*.md` | history |

None in `docs/`, READMEs, Vite config, Dockerfiles, compose files, Helm or
`infrastructure/kubernetes/**`, so no `agent:needs-human`.

## Changes

- Delete `ui/src/services/websocket.ts`.
- Drop `wsUrl` from `env.ts`, `VITE_WS_URL` from `env.d.ts` and
  `.env.example`.
- `plans/sse-write-deadline.md`: say the stream has no UI caller.
- `plans/backlog.md`: strike item 49 with `(#428)`; add item 70 for the
  `metrics/stream` endpoint that no UI code reads now.
- Old plans (#392, #399) stay as written; they record what was true then.

## Tests

In `ui/src/config/env.test.ts`:

- `config` has no `ws*` key.
- No UI source file (tests excluded) or `ui/.env.example` mentions
  `VITE_WS_URL`, read through `import.meta.glob` with `?raw`.
- A sanity check that the glob found `env.ts` and `.env.example`, so the
  scan can't pass on an empty match.

No check on the file name or on a `wsUrl` identifier: a future stream
hook could legitimately use either, and such a tripwire would fail
pointing at #428.

Mutation: with main's `env.ts` and `.env.example` restored, both of the
first two tests fail.

## Risks

- A local `.env.local` that still sets `VITE_WS_URL`: harmless, nothing
  reads it.
- Coverage: the module was never imported, so it never counted. No change
  expected.

## Verification

In `ui/`: `npm ci`, `npx tsc -b`, `npm run lint`, `npm run build`,
`npx vitest run --coverage` (lines/functions/statements 80, branches 75).
