/**
 * converterMappingWarnings (#458): which converter mappings the data-converter
 * would skip or mis-write, per applyMappings in src/cmd/data-converter/main.go.
 *
 * - every type but static and template reads `source`; without it the
 *   mapping is skipped;
 * - every type but remove writes `target`; without it the value lands under
 *   an empty key;
 * - remove deletes a top-level key only, so a dotted source removes only a
 *   top-level field with exactly that name (a literal key, not a path);
 * - a template needs an expression;
 * - an unknown type is skipped; an empty or missing type is a rename;
 * - an `each` with drop_unmapped on and no item mappings turns every item
 *   into an empty object.
 */

import { describe, it, expect } from 'vitest'
import {
  CONVERTER_MAPPING_LABELS,
  CONVERTER_MAPPING_TYPES,
  converterMappingWarnings,
  type ConverterMapping,
} from './nodeConfig'

const SOURCE = /source/i
const TARGET = /target/i
const TEMPLATE = /template/i
const UNKNOWN = /frobnicate/
// Names the item-level box: the node-level one is "Drop unmapped fields",
// and pointing at it would send the user to the wrong checkbox.
const EMPTY_ITEMS = /"Drop unmapped item fields"/
// delete(result, "customer.email") removes a literal top-level key of that
// name, so the warning must not claim it removes nothing.
const TOP_LEVEL = /only a top-level field with exactly this name/i

describe('converterMappingWarnings: valid mappings', () => {
  it.each<[string, ConverterMapping]>([
    ['rename', { type: 'rename', source: 'a', target: 'b' }],
    ['copy', { type: 'copy', source: 'a', target: 'b' }],
    ['static without a source', { type: 'static', target: 'b', value: 'x' }],
    ['static with an empty source', { type: 'static', source: '', target: 'b', value: '' }],
    ['template without a source', { type: 'template', target: 'b', expression: '{a} {b}' }],
    ['remove without a target', { type: 'remove', source: 'a' }],
    ['remove with a plain top-level source', { type: 'remove', source: 'internal_id' }],
    // Only remove is limited to top-level keys; the others read dot paths.
    ['rename with a dotted source', { type: 'rename', source: 'customer.email', target: 'email' }],
    ['copy with a dotted source', { type: 'copy', source: 'customer.email', target: 'email' }],
    ['to_string', { type: 'to_string', source: 'a', target: 'b' }],
    ['to_number', { type: 'to_number', source: 'a', target: 'b' }],
    ['each with item mappings', { type: 'each', source: 'items', target: 'lines', mappings: [{ type: 'rename', source: 'a', target: 'b' }] }],
    ['each, drop unmapped on, with item mappings', { type: 'each', source: 'items', target: 'lines', drop_unmapped: true, mappings: [{ source: 'a', target: 'b' }] }],
    ['each, drop unmapped off, no item mappings', { type: 'each', source: 'items', target: 'lines', drop_unmapped: false, mappings: [] }],
    ['each without the mappings key', { type: 'each', source: 'items', target: 'lines' }],
    ['empty type (a rename)', { type: '', source: 'a', target: 'b' }],
    ['missing type (a rename)', { source: 'a', target: 'b' }],
  ])('%s has no warning', (_name, mapping) => {
    expect(converterMappingWarnings(mapping)).toEqual([])
  })
})

describe('converterMappingWarnings: mappings the converter skips or mis-writes', () => {
  it.each<[string, ConverterMapping, RegExp[]]>([
    // Missing source, for every type that reads one.
    ['rename without a source', { type: 'rename', target: 'b' }, [SOURCE]],
    ['copy without a source', { type: 'copy', source: '', target: 'b' }, [SOURCE]],
    ['remove without a source', { type: 'remove' }, [SOURCE]],
    ['to_string without a source', { type: 'to_string', target: 'b' }, [SOURCE]],
    ['to_number without a source', { type: 'to_number', target: 'b' }, [SOURCE]],
    ['each without a source', { type: 'each', target: 'lines', mappings: [{ source: 'a', target: 'b' }] }, [SOURCE]],
    ['empty type without a source', { type: '', target: 'b' }, [SOURCE]],
    ['missing type without a source', { target: 'b' }, [SOURCE]],

    // Missing target, for every type that writes one.
    ['rename without a target', { type: 'rename', source: 'a' }, [TARGET]],
    ['copy without a target', { type: 'copy', source: 'a', target: '' }, [TARGET]],
    ['static without a target', { type: 'static', value: 'x' }, [TARGET]],
    ['template without a target', { type: 'template', expression: '{a}' }, [TARGET]],
    ['to_string without a target', { type: 'to_string', source: 'a' }, [TARGET]],
    ['to_number without a target', { type: 'to_number', source: 'a' }, [TARGET]],
    ['each without a target', { type: 'each', source: 'items', mappings: [{ source: 'a', target: 'b' }] }, [TARGET]],
    ['missing type without a target', { source: 'a' }, [TARGET]],

    // Both missing.
    ['rename with neither', { type: 'rename', source: '', target: '' }, [SOURCE, TARGET]],

    // remove deletes a top-level key only (delete(result, m.Source)), so a
    // dotted path is a literal key name, never a path.
    ['remove with a dotted source', { type: 'remove', source: 'customer.email' }, [TOP_LEVEL]],
    ['remove with a deeply dotted source', { type: 'remove', source: 'a.b.c' }, [TOP_LEVEL]],

    // Template without an expression.
    ['template without an expression', { type: 'template', target: 'b' }, [TEMPLATE]],
    ['template with an empty expression', { type: 'template', target: 'b', expression: '' }, [TEMPLATE]],

    // Unknown type, even with source and target set.
    ['unknown type', { type: 'frobnicate', source: 'a', target: 'b' }, [UNKNOWN]],

    // each, drop unmapped on, nothing mapped: every item becomes {}.
    ['each, drop unmapped on, empty item mappings', { type: 'each', source: 'items', target: 'lines', drop_unmapped: true, mappings: [] }, [EMPTY_ITEMS]],
    ['each, drop unmapped on, no mappings key', { type: 'each', source: 'items', target: 'lines', drop_unmapped: true }, [EMPTY_ITEMS]],
  ])('%s is flagged', (_name, mapping, expected) => {
    const warnings = converterMappingWarnings(mapping)
    expect(warnings).toHaveLength(expected.length)
    for (const re of expected) {
      expect(warnings.some((w) => re.test(w)), `a warning matching ${re} in ${JSON.stringify(warnings)}`).toBe(true)
    }
  })
})

describe('converter mapping types', () => {
  it('lists every type the converter runs, each included', () => {
    expect([...CONVERTER_MAPPING_TYPES].sort()).toEqual(
      ['copy', 'each', 'remove', 'rename', 'static', 'template', 'to_number', 'to_string'],
    )
  })

  it('has a label for every type', () => {
    for (const t of CONVERTER_MAPPING_TYPES) expect(CONVERTER_MAPPING_LABELS[t], t).toBeTruthy()
  })
})
