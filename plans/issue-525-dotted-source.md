# Rename and each leave a dotted source in place (#525)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

In the data-converter's `applyMappings` (`src/cmd/data-converter/main.go`),
`rename` and `each` read `source` as a dotted path (`getNestedField`) but,
with the node's `drop_unmapped` off, remove it with
`delete(result, m.Source)`, which only deletes a top-level key. So
`order.items` → `lines` leaves `order.items` next to the new `lines`.
Backlog item 87, found in #458. #476 softened the editor's `each` help text
to describe the duplicate.

Fix it: rename and each remove the field at the dotted path.

## Acceptance criteria

1. `rename` with a dotted source and node `drop_unmapped` off: the value is
   at `target` and no longer at `source`; the source's siblings stay.
2. `each` with a dotted source, same: the mapped array is at `target`, the
   original array is gone from `source`, siblings stay.
3. Top-level sources behave as before (key removed, value at target).
4. `drop_unmapped` on behaves as before (only mapped targets in the output).
5. `remove` is unchanged: it deletes only an exact top-level key, so
   `remove` of `a.b` leaves a nested `a.b` alone and still deletes a literal
   top-level key named `a.b`.
6. A parent left empty by the move stays as `{}` (see Decisions).
7. The input object is not mutated: a later mapping reading the same dotted
   source (for example `copy order.id`) still finds it, since mappings read
   from the input, as they do for a top-level rename today.
8. Nested `each` (an item mapping that renames a dotted field inside each
   element) removes the nested source in each element.
9. Source equal to target keeps the value; a target inside the source's
   parent (`order.id` → `order.ref`) ends with only `order.ref`.
10. Table-driven tests that fail without the fix; a mutation run on the
    deletion helper and its two call sites.
11. The editor's `each` help text no longer says the original array stays;
    its test asserts the new wording.
12. `docs/connectors/filters-converters.md` says rename and each move a
    dotted source. Backlog item 87 struck with `(#525)`.
13. From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
    `go test -race ./...`, `make lint-tenant` pass. From `ui/`:
    `npx tsc -b`, lint, `npm run build`, `npx vitest run --coverage` pass.

## Files

- `src/cmd/data-converter/main.go`: a `deleteNestedField` helper next to
  `getNestedField`/`setNestedField`; the `rename` and `each` cases call it
- `src/cmd/data-converter/dotted_source_test.go` (new): the tests
- `ui/src/components/Pipeline/ConverterMappingsEditor.tsx`: `each` help text
- `ui/src/components/Pipeline/ConverterMappingsEditor.test.tsx`: the test
  that pinned the old text
- `docs/connectors/filters-converters.md`, `plans/backlog.md`

`ui/src/types/nodeConfig.ts` needs no change: its only dotted-path warning
is for `remove`, which keeps its behaviour.

## Approach

`deleteNestedField(obj, path)` walks the path in `result`. `result` is a
shallow copy of the input, so its nested maps are the input's maps. To keep
the input intact (criterion 7) it copies each map on the way down and puts
the copy back into its parent before deleting the last key. If a segment is
missing or not an object, nothing is deleted. A path without dots is a plain
`delete`, so top-level behaviour does not change.

## Decisions

- Delete along the path rather than only documenting the duplicate: the
  issue's own preference, and `rename` means move.
- The empty parent stays (`{"order": {}}`). Pruning would remove a key the
  mapping never named, and an empty object can mean something to a
  destination. Users who want it gone add `remove order`.
- Copy-on-write instead of deleting in place, so later mappings, which read
  from the input, see the field, and so the input map is never changed.
- A literal top-level key named `order.items` is no longer deleted by a
  rename of `order.items`. That key was never what the mapping read
  (`getNestedField` splits on dots), so deleting it was part of the bug.
- `setNestedField` still writes into maps shared with the input (an
  existing quirk). Out of scope; the delete copies first, so a target inside
  the source's parent lands in the copy.
- Not labelled as a breaking contract change: the duplicate was a known bug,
  documented only as a caveat, and no shipped template uses a dotted source
  for rename or each. The PR says that output changes for anyone who relied
  on it.

## Risks

- A pipeline that read the leftover dotted field downstream loses it. Called
  out in the PR.

## Verification

Commands in criterion 13, plus the mutation run.
