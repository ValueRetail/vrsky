# Warn when the builder can't save locally (#673)

## Open questions

None. Decisions are listed under Approach and repeated in the PR.

## Goal

`saveToStorage` in `ui/src/store/canvasStore.ts` swallows a failed
`localStorage.setItem` (quota full, storage blocked) with a `console.warn`.
The tab keeps editing in memory and a reload brings back the last good save,
with no notice. The user should hear about it once.

## Acceptance criteria

1. A failed save shows one warning toast: the builder is not saving locally
   and changes are lost on reload unless the pipeline is deployed.
2. The in-memory canvas is kept and nothing throws, as today.
3. While saves keep failing there is no second toast.
4. The next successful save clears the failing state, so a later failure
   warns again.

## Files

- `ui/src/store/canvasStore.ts`: module flag `saveFailing`, toast through
  `useUIStore.getState().showWarningNotification`.
- `ui/src/store/canvasStore.test.ts`: new describe block.
- `plans/backlog.md`: strike item 157.

## Approach

- The flag lives in the module, next to `saveToStorage`, because every store
  action saves through that one function. It is not tied to a tenant: a full
  quota is a property of the browser, not of the workspace.
- The toast goes through the UI store like the other warnings (a 10 s
  duration, as the plan-required toast). `uiStore` imports only types, so the
  new import adds no cycle.
- The `console.warn` stays, for every failure.
- No retry, no banner, no change to when saves happen. That is out of scope.

## Risks

- A module-level flag carries over between tests in one file. The new tests
  start with a good save to clear it.
- If the user dismisses the toast and storage keeps failing, they are not
  warned again until a save succeeds. That is what the issue asks for.

## Verification

- Store tests: first failure warns, repeated failures do not, success then
  failure warns again, memory kept, no warning when saving works.
- Mutations, each must fail a test: remove the once-guard, remove the reset,
  never call the toast.
- `npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`.
