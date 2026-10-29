# An open UI tab learns that a newer UI is deployed (#551)

Open questions: none. Calls made without asking are under "Decisions".
Backlog item 70.

## Goal

The UI is one bundle with no lazy chunks. A tab left open across deploys
keeps running the old code against the new API; that is how #450's
"Pipeline 15:26:55" names kept arriving. The tab should notice that a newer
UI is deployed and say so, without reloading by itself (an unsaved canvas
edit is at most two seconds from its localStorage save, and the user decides
when to reload).

## Signal

Vite writes the built entry script and stylesheet into `index.html` with a
content hash in the name (`/assets/index-<hash>.js`). nginx already serves
`index.html` with `Cache-Control: no-cache, no-store, must-revalidate`
(`ui/nginx.default.conf`), and the ingress sends `/` to the UI. So the tab
compares the `/assets/` references in its own document with those in a
fresh `index.html`:

- the running tab: the `script[src]` and `link[rel=stylesheet][href]`
  elements in `document` whose URL path starts with `/assets/`;
- the deployed UI: the same elements in `GET /index.html?v=<now>` with
  `cache: 'no-store'`, parsed with `DOMParser`.

Different non-empty sets mean a newer build is live.

## Acceptance criteria

1. With the tab's assets A and a fetched `index.html` whose assets are B, a
   banner says a new version is available and offers Reload.
2. Same assets: no banner.
3. No banner when the fetch fails, answers non-2xx (ingress error page
   while the cluster is parked or rolling), or the fetched page has no
   `/assets/` references. The next check still runs.
4. The tab checks every 5 minutes while visible, and when it becomes
   visible or focused again if the last check (page load counts as one) is
   more than a minute old. A hidden tab doesn't poll on the timer.
5. Every request bypasses caches: `cache: 'no-store'` and a query string
   that changes per request.
6. Reload calls `window.location.reload()`; nothing reloads without a click.
7. Dismiss hides the banner for that version. A later, different version
   shows it again.
8. A document with no `/assets/` references (the Vite dev server, tests)
   never polls.
9. The banner renders on every route, the builder included (it isn't under
   `RootLayout`), so it mounts in `App`.
10. Unmounting stops the timer and listeners, and a late answer sets no
    state.
11. Tests (vitest and testing-library, `fetch` stubbed, fake timers) cover
    1 to 10 and fail without the fix. Mutation run on the comparison, the
    response checks, the visibility gate and the dismiss rule.
12. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
    `npx vitest run --coverage` pass with thresholds held.
13. Backlog item 70 struck with `(#551)`.

## Files

- `ui/src/utils/buildAssets.ts` (new): read the asset set from a document
  or HTML text.
- `ui/src/hooks/useNewVersion.ts` (new): the polling hook.
- `ui/src/components/Common/NewVersionBanner.tsx` (new): the banner.
- `ui/src/App.tsx`: mount the banner.
- tests beside each new file, and `ui/src/App.test.tsx`.
- `docs/operator/deploy.md`
- `plans/backlog.md` (item 70 struck, item 115 added)

No change to `ui/Dockerfile`, the nginx config or `infrastructure/`.

## Decisions

- The hashed asset names in `index.html` instead of the backlog's
  `version.json` with the commit. The Docker build context is `ui/` without
  `.git`, so the commit would need a build argument passed by the deploy
  scripts in `infrastructure/azure/` (a human-gated path). A build id made up
  at build time would change on every image build, so a UI merge that
  changes nothing shipped (a test, a comment) would still show the banner.
  The asset hashes change exactly when the shipped UI does, need no new file,
  and nginx already serves `index.html` uncached.
- Only references under `/assets/` count. A browser extension that injects
  a script into the live page would otherwise look like a new version.
- Polling keeps going after a version is found, so a dismissed banner can
  come back for a later deploy. It is one small request every 5 minutes.
- During a rolling update two pods may answer with different builds for a
  moment. The banner may then show a little early; reloading still lands on
  either the old or the new build and the next check settles it. One
  failure is worse: a new pod's `index.html` with its script then fetched
  from an old pod gives a blank page until the next reload. That skew
  exists without this change too; it is backlog item 115.
- No in-flight guard on the check. With one, a request that never
  settles stopped every later check for good (found by the test author);
  the one-minute focus gap and the 5-minute timer already keep requests
  from piling up.
- Docs: one paragraph in `docs/operator/deploy.md`.
- Tests: the late-answer test wraps React's `useState` through
  `vi.mock('react')` to count setter calls, because React 18 drops an
  update on an unmounted component without a trace. It is the only test
  that catches a missing `cancelled` check.
