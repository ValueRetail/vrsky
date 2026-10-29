# Converter editor accessibility (#542)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

In `ui/src/components/Pipeline/ConverterMappingsEditor.tsx` every mapping
row, nested ones included, has a select named "Mapping type" and a button
named "Remove mapping". A screen reader user hears the same names for every
row and can't tell which row they're on or which one a button removes. The
row's warning list isn't tied to its inputs either, so moving to an input
doesn't read the warnings that apply to it. Found in #458.

## Acceptance criteria

1. Each row's type select and remove button name the row by position and,
   once it has a source or target, by what it maps:
   - top level: "Type of mapping 2 (items → lines)",
     "Remove mapping 2 (items → lines)";
   - inside an `each`: "Type of item mapping 1 of mapping 2 (sku → code)",
     nested deeper the same way;
   - only a source or only a target: just that one in brackets
     ("Remove mapping 1 (internal_id)"); neither: no brackets.
   Every select and button name in the editor is unique.
2. Each row is a group named after it ("Mapping 2 (items → lines)"), so
   the repeated "Source Field" and "Target Field" inputs have row context.
3. When a row has warnings, its type select, its text inputs (source,
   target, value, template) and an `each` row's "Drop unmapped item fields"
   checkbox have `aria-describedby` pointing at the items of that row's
   warning list (and only that row's). A row without warnings has no
   `aria-describedby`. The link follows edits: fixing the last warning
   removes it, causing one adds it.
4. Names follow edits (typing a target updates the names) and positions
   follow removals (removing mapping 1 makes the old mapping 2 "mapping 1").
5. Tests from these criteria fail without the fix; mutation run.
6. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
7. Backlog item 88 struck with `(#542)`.

## Files

- `ui/src/components/Pipeline/ConverterMappingsEditor.tsx`
- `ui/src/components/Pipeline/StyledFields.tsx`: optional `describedBy` on
  `StyledInput`
- `ui/src/components/Pipeline/ConverterMappingsEditor.test.tsx`
- `plans/backlog.md`

## Decisions

- Position plus what the row maps, as the issue suggests. Position alone
  is unique but says little; the target alone isn't unique (two rows can
  write the same target) and is empty on a new row.
- Nested rows say whose items they are ("item mapping 1 of mapping 2"), so
  names stay unique across several `each` rows.
- The row group is added as well. It costs one attribute and gives the
  visible "Source Field" and "Target Field" labels their row context
  without renaming them.
- The warning list keeps its name "Mapping warnings" and sits inside the
  named row group. The inputs point at its items, not at the list: a
  browser builds the description from a referenced element's `aria-label`
  when it has one, so pointing at the list would read "Mapping warnings"
  instead of the warnings. jsdom uses the text either way, so the tests
  also check the referenced elements carry no `aria-label`. Found by the
  test author.
- The existing tests looked the select and button up by the old exact
  names. They now match the new names by prefix ("Type of … mapping N",
  "Remove … mapping N"), which finds the same elements; no assertion
  changed.
