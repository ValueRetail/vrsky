# A reload during a UI rollout must not land on a blank page (#600)

Open questions: none. Calls made without asking are under "Decisions".
Backlog item 115. Builds on `plans/issue-551-new-version.md`.

## Problem

With three UI replicas and `maxSurge: 1`, `index.html` from a new pod names
`/assets/index-<new>.js`. An old pod does not have that file; its
`error_page 404 /index.html` answers with HTML, the browser refuses it as a
module script and the page stays blank. The #551 banner could show as soon
as the first new pod served, so a quick Reload could hit exactly this.

## Acceptance criteria

1. The banner offers Reload only after two checks at least 30 s apart have
   seen the same new asset set.
2. One check that sees a new set offers nothing. A set that changes between
   the checks, a rollback to the tab's own set, or a failed check between
   the sightings starts the wait again.
3. Once offered, a version stays offered through a failed check (as in
   #551); Later still hides it for that version; a later, different version
   shows it again after its own two checks.
4. The confirming check does not wait for the 5-minute timer: it runs 31 s
   after the first sighting, and only while the tab is visible (a hidden tab
   still makes no request; the focus/visibility check confirms later).
5. A Reload that still lands on a page whose `/assets/` script fails to load
   recovers with one reload after a short wait, and never loops.
6. Tests with fake timers fail before the change and pass after it; the
   existing #551 behaviour (Later, focus gap, visibility, unmount) holds.
7. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass with thresholds held.
8. Backlog item 115 struck with `(#600)`; the paragraph in
   `docs/operator/deploy.md` updated.

## Files

- `ui/src/hooks/useNewVersion.ts`: the two-check rule.
- `ui/index.html`: the inline retry guard.
- `ui/src/utils/assetRetry.ts` (new) and `ui/src/main.tsx`: clear the guard's
  flag once the app has started.
- tests: `useNewVersion.test.tsx`, `NewVersionBanner.test.tsx`,
  `utils/assetRetry.test.ts` (new).
- `docs/operator/deploy.md`, `plans/backlog.md`.

No change to `ui/Dockerfile`, the nginx config or `infrastructure/`.

## Decisions

- Confirmation by the page, not by new files in the image. Keeping the
  previous build's assets in the new image is the other fix from the backlog
  item, but it needs a Dockerfile change (a human-gated path). Waiting for
  two matching checks needs none. It narrows the window rather than closing
  it, which is why the retry guard exists too.
- A first sighting schedules one extra check 31 s later (30 s minimum gap
  plus a second, because browser timers can fire a hair early against
  `Date.now()`). Without it the banner would wait for the next 5-minute tick
  and the "within five minutes" promise of #551 would become ten. The extra
  check follows the same rules as the timer: not while the tab is hidden.
- The 30 s gap is checked as well as scheduled. A focus check can land
  within 30 s of a timer check; those two do not count as a pair.
- A failed or empty answer clears the first sighting. A rollout that
  produces ingress errors is exactly when two answers should not be paired
  across the gap. An already offered version is kept, so a blip does not
  make the banner flicker.
- A set that changes between sightings clears the offer too: a Reload
  offered for B while C is rolling out is the case this change is about.
- The guard is an inline classic `<script>` in `ui/index.html`, ahead of the
  module script. Vite leaves an inline classic script alone, the module
  script's own `onerror` is not reachable from the bundle that failed to
  load, and a capturing `error` listener on `window` sees the script
  element's failed load (the event does not bubble). It only reacts to a
  `<script>` from this origin under `/assets/`, so an extension's injected
  script cannot trigger a reload.
- The retry is `location.reload()` after 2 s: the browser fetches
  `index.html` again, and by then the rollout has usually moved on. Once per
  tab: a `sessionStorage` flag is set before the reload and cleared by
  `src/main.tsx` after the app has started, so a second failure in a row
  stops and a later rollout in the same tab can retry. With no usable
  `sessionStorage` it does not retry, because it could not stop a loop.
- Only scripts, not the stylesheet. If the script loads and only the CSS
  fails, the app starts and clears the flag; a guard on the stylesheet
  would then retry on every load for as long as the CSS failed.
- The guard's test reads the script out of `index.html` (`?raw`) and runs
  it in jsdom, so the shipped text is what is tested.
- Mutation run (by hand) on the gap check, the already-offered check, the
  same-set comparison, the flag check and the origin check; each is caught
  by at least one test.
- The guard skips its reload when `#root` already has children after the
  2 s wait: if the app started, a second failing script (a lazy chunk, say)
  must not reload the page.

## Limits

- The guard is an inline script, so it depends on the UI serving no
  Content-Security-Policy that blocks inline scripts. True today:
  `ui/nginx.conf`, `ui/nginx.default.conf` and the UI ingress set none.
  Adding a CSP later needs a nonce or hash for this script, or the guard
  silently stops working.
- The rollout window is narrowed, not closed. Old and new pods can both
  answer two checks 31 s apart, so the banner can still be offered for a
  set whose script an old pod 404s. The entry-script retry covers that
  case after the fact.
