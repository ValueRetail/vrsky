# Converter editor: `each` and `split_records` (#458)

The data-converter has two settings the builder's converter editor does not
know (backlog item 63, found during #435):

- an `each` mapping type, with its own nested `mappings` and `drop_unmapped`;
- `split_records` on the node.

Open the POS sales template's converter today and the `each` row shows as
"Rename" (the select has no `each` option), its nested lines are invisible,
and `split_records` is not shown at all.

## What the server does (the spec)

From `src/cmd/data-converter/main.go` and `streaming.go`:

| Setting | Behaviour |
|---|---|
| `type: "each"` | Reads `source` (dot path) as an array, applies the nested `mappings` (any type, `each` included) to every object element with the nested `drop_unmapped`, keeps non-object elements, writes the array to `target`. With the node's `drop_unmapped` off, the source key is removed (same as rename). A missing or non-array source is skipped. |
| `type: ""` | Treated as `rename`. |
| any other type | Skipped without a word. |
| `split_records: true` | An array result is published as one message per record; a single object goes out as one message. Works with every output format. A payload over the rehydrate cap (streamed path) is declined: NAK, then DLQ. |
| missing source on rename/copy/to_string/to_number/each/remove | Mapping skipped (`getNestedField` finds nothing). |
| missing target on rename/copy/static/template/to_string/to_number/each | Value written under an empty key `""`. |
| template with no expression | Skipped. |

The management API does no save-time validation of mappings or
`split_records` (`validateTransformNodeConfig` only checks the XML record
path). So "rejected" here means what the converter skips, mis-writes or
declines at run time. The UI flags those as warnings next to the row and
never blocks a save, so the editor and the server agree on what can be saved.

## Acceptance criteria

1. The mapping type select offers every type in `CONVERTER_MAPPING_TYPES`,
   `each` included. A type the converter does not know is shown as itself
   (marked unknown), not as "Rename".
2. An `each` row shows source, target, its own "drop unmapped" checkbox and
   its nested mappings, which can be added, edited and removed, nested `each`
   included.
3. A "split records" checkbox shows and sets `split_records`.
4. A config loaded with `each` and `split_records` and saved without edits is
   unchanged, including keys the editor does not render.
5. Changing a row's type keeps its other keys (an `each` row switched away
   and back gets its lines back).
6. Rows the converter would skip or mis-write get a warning: a missing
   source or target for the types that read them, a template without an
   expression, an unknown type, a `remove` with a dotted source (the
   converter deletes top-level keys only), an `each` with "drop unmapped" on and no
   nested mappings (every line becomes `{}`). `split_records` shows a note
   that payloads over the rehydrate cap go to the DLQ.
7. A Go guard test fails when the converter's mapping types or config keys
   and the UI's lists drift apart.

## Files

| File | Change |
|---|---|
| `ui/src/types/nodeConfig.ts` | `ConverterMapping` type; labels for mapping types; `converterMappingWarnings` |
| `ui/src/components/Pipeline/ConverterMappingsEditor.tsx` (new) | mapping rows (recursive for `each`), add/remove, drop unmapped, split records |
| `ui/src/components/Pipeline/PropertyEditor.tsx` | `ConverterConfig` uses the new section; the drag-and-drop wrappers stay here |
| `ui/src/components/Pipeline/ConverterMappingsEditor.test.tsx` (new) | tests from the criteria above (separate test author) |
| `src/cmd/data-converter/ui_guard_test.go` (new) | drift guard: mapping types from the `applyMappings` switch, config keys from the struct tags, against `nodeConfig.ts` |
| `docs/connectors/filters-converters.md` | both settings are in the editor now |
| `plans/backlog.md` | strike item 63 (#458) |

`src/pkg/managementapi/nodeconfig_test.go` has no converter guard to extend
(its drift guard covers source and destination types), so the guard sits
next to the code it reads, in the data-converter package.

## Approach

Pull the mapping list out of the 4,000-line `PropertyEditor.tsx` into its own
file, as `RemoteAgentConfigEditor.tsx` did, so it can be tested without the
whole editor. The section takes `config` and `setConfig` and always writes
`{...config, …}` and `{...mapping, …}`, so keys it does not render survive.
Drag and drop stays in `PropertyEditor` and is passed in as optional row and
add-button wrappers; nested rows have no drop target. The row wrapper holds
only the row's own fields, not an `each` row's item mappings: the review
found that a field dropped on an item otherwise landed on the `each` row and
replaced its source array.

The preview endpoint (`/preview/` on the converter) ignores `split_records`,
so the preview still shows the unsplit result. Not changed: it is a server
change and outside this issue.

## Risks

- Moving the mapping JSX out of `PropertyEditor` could break drag and drop.
  Kept the same `DroppableMapping` and `DropToAddMapping` wrappers around the
  same indices.
- Warnings on a fresh row (empty source and target) are noise for a second.
  They are short and inline, and it is what the converter would do.

## Verification

- `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
  `npx vitest run --coverage`.
- `src/`: `go vet ./cmd/data-converter/`, `go test -count=1 ./cmd/data-converter/`.
- Mutation run: drop `each` from the select, stop spreading the old mapping,
  stop writing `split_records`, remove a warning rule; each must fail a test.
