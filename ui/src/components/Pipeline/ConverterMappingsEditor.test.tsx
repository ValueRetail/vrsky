/**
 * The converter node editor's mapping section (#458).
 *
 * The data-converter runs an `each` mapping type (nested item mappings with
 * their own drop_unmapped) and a node-level `split_records`. These check the
 * editor shows and edits both, keeps every key it does not render, and flags
 * the rows the converter would skip or mis-write without blocking anything.
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { useState, type ReactNode } from 'react'
import ConverterMappingsEditor from './ConverterMappingsEditor'
import { CONVERTER_MAPPING_TYPES } from '@/types/nodeConfig'
import sitooToBusinessCentral from '../../onboarding/testdata/sitoo-to-business-central.json'

type Config = Record<string, unknown>
type Mapping = Record<string, unknown>

// Renders the section the way PropertyEditor does: the host owns the config
// and hands the section setConfig. onChange sees every config produced.
function renderEditor(initial: Config, props: {
  wrapRow?: (i: number, row: ReactNode) => ReactNode
  wrapAdd?: (button: ReactNode) => ReactNode
} = {}) {
  const onChange = vi.fn<(c: Config) => void>()
  function Host() {
    const [config, setConfig] = useState<Config>(initial)
    return (
      <ConverterMappingsEditor
        config={config}
        setConfig={(c) => { setConfig(c); onChange(c) }}
        {...props}
      />
    )
  }
  render(<Host />)
  const last = (): Config => {
    expect(onChange).toHaveBeenCalled()
    return onChange.mock.calls[onChange.mock.calls.length - 1][0]
  }
  return { onChange, last }
}

// The POS sales template's converter config, plus keys the editor does not
// render: node formats, and unknown extra keys on a mapping and an item.
function templateConfig(): Config {
  const c = structuredClone(sitooToBusinessCentral) as unknown as Config
  c.input_format = 'json'
  c.output_format = 'json'
  const mappings = c.mappings as Mapping[]
  mappings[1].note = 'keep me'
  const lines = mappings[2].mappings as Mapping[]
  lines[1].x_custom = { nested: [1, 2] }
  return c
}

// Every row's type select and remove button, whatever row they belong to;
// the names carry the row since #542 ("Type of mapping 2 (items → lines)").
const MAPPING_TYPE = /^Type of (item )?mapping \d/
const REMOVE_MAPPING = /^Remove (item )?mapping \d/
const typeSelects = () => screen.getAllByLabelText(MAPPING_TYPE) as HTMLSelectElement[]
const optionValues = (s: HTMLSelectElement) => Array.from(s.options).map((o) => o.value)
const selectedText = (s: HTMLSelectElement) => s.options[s.selectedIndex]?.textContent ?? ''
const itemGroups = () => screen.queryAllByRole('group', { name: 'Item mappings' })
const warningLists = (root: HTMLElement = document.body) =>
  within(root).queryAllByRole('list', { name: 'Mapping warnings' })
const mappingsOf = (c: Config) => c.mappings as Mapping[]

describe('mapping type select', () => {
  it('offers every converter mapping type, each included', () => {
    renderEditor({ mappings: [{ type: 'rename', source: 'a', target: 'b' }] })
    const [select] = typeSelects()
    expect(optionValues(select)).toEqual([...CONVERTER_MAPPING_TYPES])
    expect(optionValues(select)).toContain('each')
  })

  it('shows an each row as each, not as rename', () => {
    renderEditor(templateConfig())
    expect(typeSelects()[2].value).toBe('each')
  })

  it('shows an unknown type as itself, marked unknown, not as Rename', () => {
    renderEditor({ mappings: [{ type: 'frobnicate', source: 'a', target: 'b' }] })
    const [select] = typeSelects()
    expect(select.value).toBe('frobnicate')
    expect(selectedText(select)).toContain('frobnicate')
    expect(selectedText(select)).toMatch(/unknown/i)
    expect(selectedText(select)).not.toBe('Rename')
    // The known types are still on offer.
    for (const t of CONVERTER_MAPPING_TYPES) expect(optionValues(select)).toContain(t)
  })

  it.each<[string, Mapping]>([
    ['missing', { source: 'a', target: 'b' }],
    ['empty', { type: '', source: 'a', target: 'b' }],
  ])('shows a %s type as rename, with no unknown option', (_name, mapping) => {
    renderEditor({ mappings: [mapping] })
    const [select] = typeSelects()
    expect(select.value).toBe('rename')
    expect(Array.from(select.options).some((o) => /unknown/i.test(o.textContent ?? ''))).toBe(false)
  })

  it('switching a row to each shows the item mappings', () => {
    const { last } = renderEditor({ mappings: [{ type: 'rename', source: 'items', target: 'lines' }] })
    expect(itemGroups()).toHaveLength(0)
    fireEvent.change(typeSelects()[0], { target: { value: 'each' } })
    expect(mappingsOf(last())[0]).toEqual({ type: 'each', source: 'items', target: 'lines' })
    expect(itemGroups()).toHaveLength(1)
  })
})

describe('each row', () => {
  it('shows source, target, its own drop unmapped and its item mappings', () => {
    renderEditor(templateConfig())
    expect(screen.getByLabelText('Source Array')).toHaveValue('items')
    const [group] = itemGroups()
    expect(group).toBeDefined()
    // The each row's own target sits outside its item list.
    const targets = (screen.getAllByLabelText('Target Field') as HTMLInputElement[]).map((i) => i.value)
    expect(targets).toContain('salesInvoiceLines')
    expect(within(group).getByLabelText('Drop unmapped item fields')).toBeChecked()
    // Four item mappings, with their types and fields.
    expect(within(group).getAllByLabelText(MAPPING_TYPE).map((s) => (s as HTMLSelectElement).value))
      .toEqual(['static', 'rename', 'to_number', 'to_number'])
    expect(within(group).getAllByLabelText('Target Field').map((i) => (i as HTMLInputElement).value))
      .toEqual(['lineType', 'lineObjectNumber', 'quantity', 'unitPrice'])
    expect(within(group).getByLabelText('Value')).toHaveValue('Item')
  })

  // Since #525 each removes its source along a dotted path (deleteNestedField),
  // so the help text says the source array goes, dotted or not, and no longer
  // warns that the original stays in place.
  it('says the source array is removed, dotted path included', () => {
    renderEditor(templateConfig())
    const [group] = itemGroups()
    expect(group.textContent).not.toMatch(/stays in place/i)
    expect(group.textContent).toMatch(/source array is removed, also when it is a dotted path/i)
  })

  it('edits an item mapping field', () => {
    const { last } = renderEditor(templateConfig())
    const [group] = itemGroups()
    fireEvent.change(within(group).getAllByLabelText('Source Field')[0], { target: { value: 'itemsku' } })
    // The static item has no source field, so the first one is the rename.
    const each = mappingsOf(last())[2]
    expect((each.mappings as Mapping[])[1]).toEqual({ type: 'rename', source: 'itemsku', target: 'lineObjectNumber', x_custom: { nested: [1, 2] } })
  })

  it('adds an item mapping', () => {
    const { last } = renderEditor(templateConfig())
    const [group] = itemGroups()
    fireEvent.click(within(group).getByRole('button', { name: /add item mapping/i }))
    const items = mappingsOf(last())[2].mappings as Mapping[]
    expect(items).toHaveLength(5)
    expect(items.slice(0, 4)).toEqual((templateConfig().mappings as Mapping[])[2].mappings)
    // The new row is editable straight away.
    expect(within(itemGroups()[0]).getAllByLabelText(MAPPING_TYPE)).toHaveLength(5)
    fireEvent.change(within(itemGroups()[0]).getAllByLabelText('Target Field')[4], { target: { value: 'description' } })
    expect((mappingsOf(last())[2].mappings as Mapping[])[4].target).toBe('description')
    // Top-level mappings are untouched.
    expect(mappingsOf(last())).toHaveLength(3)
  })

  it('removes an item mapping', () => {
    const { last } = renderEditor(templateConfig())
    const [group] = itemGroups()
    fireEvent.click(within(group).getAllByRole('button', { name: REMOVE_MAPPING })[0])
    const original = (templateConfig().mappings as Mapping[])[2].mappings as Mapping[]
    expect(mappingsOf(last())[2].mappings).toEqual(original.slice(1))
    expect(mappingsOf(last())).toHaveLength(3)
    expect(within(itemGroups()[0]).getAllByLabelText(MAPPING_TYPE)).toHaveLength(3)
  })

  it('sets its own drop unmapped, separate from the node one', () => {
    const { last } = renderEditor(templateConfig())
    fireEvent.click(within(itemGroups()[0]).getByLabelText('Drop unmapped item fields'))
    expect(mappingsOf(last())[2].drop_unmapped).toBe(false)
    expect(last().drop_unmapped).toBe(true)
  })

  it('supports a nested each with its own item mappings', () => {
    const { last } = renderEditor(templateConfig())
    const outer = itemGroups()[0]
    // Turn the second item mapping into an each.
    fireEvent.change(within(outer).getAllByLabelText(MAPPING_TYPE)[1], { target: { value: 'each' } })
    expect(itemGroups()).toHaveLength(2)
    const inner = itemGroups()[1]
    fireEvent.click(within(inner).getByRole('button', { name: /add item mapping/i }))
    fireEvent.change(within(itemGroups()[1]).getByLabelText('Source Field'), { target: { value: 'code' } })
    const nested = (mappingsOf(last())[2].mappings as Mapping[])[1]
    expect(nested.type).toBe('each')
    expect(nested.source).toBe('sku')
    expect(nested.x_custom).toEqual({ nested: [1, 2] })
    expect(nested.mappings as Mapping[]).toHaveLength(1)
    expect((nested.mappings as Mapping[])[0].source).toBe('code')
    // And the inner item can be removed again.
    fireEvent.click(within(itemGroups()[1]).getByRole('button', { name: REMOVE_MAPPING }))
    expect((mappingsOf(last())[2].mappings as Mapping[])[1].mappings).toEqual([])
  })
})

describe('split records', () => {
  it('shows split_records', () => {
    renderEditor(templateConfig())
    expect(screen.getByLabelText(/split records/i)).toBeChecked()
  })

  it('is off when split_records is missing', () => {
    renderEditor({ mappings: [] })
    expect(screen.getByLabelText(/split records/i)).not.toBeChecked()
  })

  it('sets and clears split_records, keeping the rest of the config', () => {
    const { last } = renderEditor({ input_format: 'json', output_format: 'xml', mappings: [] })
    fireEvent.click(screen.getByLabelText(/split records/i))
    expect(last()).toEqual({ input_format: 'json', output_format: 'xml', mappings: [], split_records: true })
    expect(screen.getByLabelText(/split records/i)).toBeChecked()
    fireEvent.click(screen.getByLabelText(/split records/i))
    expect(last().split_records).toBe(false)
  })

  it('notes that payloads over the rehydrate cap go to the dead-letter queue, only when on', () => {
    renderEditor({ mappings: [] })
    expect(screen.queryByText(/dead-letter|DLQ/i)).toBeNull()
    fireEvent.click(screen.getByLabelText(/split records/i))
    const note = screen.getByText(/dead-letter|DLQ/i)
    expect(note.textContent).toMatch(/rehydrate cap/i)
  })
})

describe('round trip', () => {
  it('loads the template config without writing anything', () => {
    const { onChange } = renderEditor(templateConfig())
    expect(onChange).not.toHaveBeenCalled()
  })

  it('an edit undone gives back the exact config, unrendered keys included', () => {
    const original = templateConfig()
    const { last } = renderEditor(original)
    const [group] = itemGroups()
    const target = within(group).getAllByLabelText('Target Field')[1]
    fireEvent.change(target, { target: { value: 'somethingElse' } })
    fireEvent.change(target, { target: { value: 'lineObjectNumber' } })
    fireEvent.click(screen.getByLabelText(/split records/i))
    fireEvent.click(screen.getByLabelText(/split records/i))
    expect(last()).toEqual(templateConfig())
    // The config passed in was not mutated.
    expect(original).toEqual(templateConfig())
  })

  it('editing one nested field changes only that field', () => {
    const { last } = renderEditor(templateConfig())
    const [group] = itemGroups()
    fireEvent.change(within(group).getAllByLabelText('Target Field')[3], { target: { value: 'unitCost' } })
    const expected = templateConfig()
    ;((mappingsOf(expected)[2].mappings as Mapping[])[3]).target = 'unitCost'
    expect(last()).toEqual(expected)
  })

  it('editing a top-level field keeps unknown keys on that row', () => {
    const { last } = renderEditor(templateConfig())
    fireEvent.change(screen.getAllByLabelText('Source Field')[0], { target: { value: 'receiptno' } })
    const expected = templateConfig()
    mappingsOf(expected)[1].source = 'receiptno'
    expect(last()).toEqual(expected)
  })
})

describe('changing a type keeps the row', () => {
  it('an each row switched to rename and back still has its item mappings', () => {
    const { last } = renderEditor(templateConfig())
    const eachSelect = typeSelects()[2]
    fireEvent.change(eachSelect, { target: { value: 'rename' } })
    const asRename = mappingsOf(last())[2]
    expect(asRename.type).toBe('rename')
    expect(asRename.mappings).toEqual((templateConfig().mappings as Mapping[])[2].mappings)
    expect(asRename.drop_unmapped).toBe(true)
    expect(itemGroups()).toHaveLength(0)

    fireEvent.change(typeSelects()[2], { target: { value: 'each' } })
    expect(last()).toEqual(templateConfig())
    expect(within(itemGroups()[0]).getAllByLabelText(MAPPING_TYPE)).toHaveLength(4)
  })

  it('a static row switched to template and back keeps its value', () => {
    const { last } = renderEditor({ mappings: [{ type: 'static', source: '', target: 'lineType', value: 'Item' }] })
    fireEvent.change(typeSelects()[0], { target: { value: 'template' } })
    expect(mappingsOf(last())[0]).toMatchObject({ type: 'template', value: 'Item', target: 'lineType' })
    fireEvent.change(typeSelects()[0], { target: { value: 'static' } })
    expect(mappingsOf(last())[0]).toEqual({ type: 'static', source: '', target: 'lineType', value: 'Item' })
    expect(screen.getByLabelText('Value')).toHaveValue('Item')
  })
})

describe('warnings', () => {
  it('shows no warning on the valid template config', () => {
    renderEditor(templateConfig())
    expect(warningLists()).toHaveLength(0)
  })

  it.each<[string, Mapping, RegExp]>([
    ['a rename without a source', { type: 'rename', target: 'b' }, /source/i],
    ['a copy without a target', { type: 'copy', source: 'a' }, /target/i],
    ['a template without an expression', { type: 'template', target: 'b' }, /template/i],
    ['an unknown type', { type: 'frobnicate', source: 'a', target: 'b' }, /frobnicate/],
    ['a remove with a dotted source', { type: 'remove', source: 'customer.email' }, /only a top-level field with exactly this name/i],
    ['an each with drop unmapped and no item mappings', { type: 'each', source: 'items', target: 'lines', drop_unmapped: true, mappings: [] }, /"Drop unmapped item fields" is on/],
  ])('flags %s', (_name, mapping, re) => {
    renderEditor({ mappings: [mapping] })
    const lists = warningLists()
    expect(lists).toHaveLength(1)
    expect(lists[0].textContent).toMatch(re)
  })

  it('flags an item mapping inside its each row', () => {
    const config = templateConfig()
    delete ((mappingsOf(config)[2].mappings as Mapping[])[2]).source
    renderEditor(config)
    const lists = warningLists()
    expect(lists).toHaveLength(1)
    expect(itemGroups()[0]).toContainElement(lists[0])
    expect(lists[0].textContent).toMatch(/source/i)
  })

  it('turning on drop unmapped for an each with no item mappings raises the warning, adding one clears it', () => {
    renderEditor({ mappings: [{ type: 'each', source: 'items', target: 'lines', mappings: [] }] })
    expect(warningLists()).toHaveLength(0)
    fireEvent.click(screen.getByLabelText('Drop unmapped item fields'))
    expect(warningLists()).toHaveLength(1)
    fireEvent.click(within(itemGroups()[0]).getByRole('button', { name: /add item mapping/i }))
    const group = itemGroups()[0]
    fireEvent.change(within(group).getByLabelText('Source Field'), { target: { value: 'sku' } })
    fireEvent.change(within(group).getByLabelText('Target Field'), { target: { value: 'code' } })
    expect(warningLists()).toHaveLength(0)
  })

  it('never blocks an edit or the add button', () => {
    const { last } = renderEditor({ mappings: [{ type: 'frobnicate', source: '' }] })
    expect(warningLists()).toHaveLength(1)
    for (const el of screen.getAllByRole('button')) expect(el).toBeEnabled()
    for (const el of screen.getAllByRole('textbox')) expect(el).toBeEnabled()
    fireEvent.change(screen.getByLabelText('Target Field'), { target: { value: 'b' } })
    expect(mappingsOf(last())[0]).toEqual({ type: 'frobnicate', source: '', target: 'b' })
    fireEvent.click(screen.getByRole('button', { name: '+ Add Mapping' }))
    expect(mappingsOf(last())).toHaveLength(2)
    expect(mappingsOf(last())[0]).toEqual({ type: 'frobnicate', source: '', target: 'b' })
  })
})

describe('row wrappers', () => {
  it('wraps each top-level row by index and the add button, never item rows', () => {
    const wrapRow = vi.fn((i: number, row: ReactNode) => <div data-testid={`row-${i}`}>{row}</div>)
    const wrapAdd = vi.fn((button: ReactNode) => <div data-testid="add">{button}</div>)
    renderEditor(templateConfig(), { wrapRow, wrapAdd })
    expect(screen.getAllByTestId(/^row-/).map((el) => el.dataset.testid)).toEqual(['row-0', 'row-1', 'row-2'])
    expect(within(screen.getByTestId('add')).getByRole('button', { name: '+ Add Mapping' })).toBeInTheDocument()
    expect(within(itemGroups()[0]).queryAllByTestId(/^row-|^add$/)).toHaveLength(0)
  })

  // PropertyEditor's wrapRow is a drop target: a schema field dropped on
  // mapping:i sets row i's source. If an each row's item mappings sat inside
  // it, a field dropped on an item would overwrite the each row's source.
  it("holds only the row's own fields, never an each row's item mappings or its warnings", () => {
    const wrapRow = (i: number, row: ReactNode) => <div data-testid={`drop-${i}`}>{row}</div>
    const config = templateConfig()
    // A warning on the each row (no target), so its placement is checked too.
    delete mappingsOf(config)[2].target
    renderEditor(config, { wrapRow })

    const drop = screen.getByTestId('drop-2')
    const group = itemGroups()[0]
    // The each row's own fields are in its drop target.
    expect(drop).toContainElement(screen.getByLabelText('Source Array'))
    expect(within(drop).getAllByLabelText(MAPPING_TYPE)).toHaveLength(1)
    expect((within(drop).getByLabelText(MAPPING_TYPE) as HTMLSelectElement).value).toBe('each')
    expect(within(drop).getAllByLabelText('Target Field')).toHaveLength(1)
    // Its item mappings, their add button, its drop-unmapped and its
    // warnings are not.
    expect(drop).not.toContainElement(group)
    expect(drop.contains(group)).toBe(false)
    expect(group.contains(drop)).toBe(false)
    expect(within(drop).queryByRole('button', { name: /add item mapping/i })).toBeNull()
    expect(within(drop).queryByLabelText('Drop unmapped item fields')).toBeNull()
    expect(within(drop).queryByLabelText('Source Field')).toBeNull()
    const warnings = warningLists()
    expect(warnings).toHaveLength(1)
    expect(drop).not.toContainElement(warnings[0])

    // No top-level drop target holds an item row.
    for (const el of screen.getAllByTestId(/^drop-/)) {
      for (const itemSelect of within(group).getAllByLabelText(MAPPING_TYPE)) {
        expect(el).not.toContainElement(itemSelect)
      }
    }
    // The other rows' own fields are in their own drop targets.
    expect(within(screen.getByTestId('drop-1')).getByLabelText('Source Field')).toHaveValue('receiptnumber')
    expect(within(screen.getByTestId('drop-0')).getByLabelText('Value')).toHaveValue('')
  })
})

/**
 * Screen reader names and warnings (#542). Every row used to have a select
 * named "Mapping type" and a button named "Remove mapping", and its warning
 * list was not tied to its inputs.
 */
describe('row names and warning links (#542)', () => {
  // The row a control or warning list belongs to: its nearest row group,
  // skipping the "Item mappings" group an each row puts around its items.
  function rowOf(el: Element): Element | null {
    for (let cur = el.parentElement; cur; cur = cur.parentElement) {
      if (cur.getAttribute('role') === 'group' && cur.getAttribute('aria-label') !== 'Item mappings') return cur
    }
    return null
  }
  const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
  const rowGroup = (name: string) => screen.getByRole('group', { name: capitalize(name) })
  // A row's own select, text inputs and checkbox, not those of its items.
  const ownControls = (row: HTMLElement) =>
    (['combobox', 'textbox', 'checkbox'] as const)
      .flatMap((role) => within(row).queryAllByRole(role))
      .filter((el) => rowOf(el) === row)
  const ownWarningList = (row: HTMLElement) => {
    const lists = warningLists(row).filter((l) => rowOf(l) === row)
    expect(lists).toHaveLength(1)
    return lists[0]
  }
  // The elements aria-describedby points at, in order; null when unset.
  const describedElements = (el: Element): (HTMLElement | null)[] | null => {
    const ids = el.getAttribute('aria-describedby')
    if (ids === null) return null
    return ids.trim().split(/\s+/).map((id) => document.getElementById(id))
  }
  // Every own control of the row points at each item of the row's own warning
  // list, and reads its warning. It points at the items rather than the list:
  // browsers describe by a referenced element's aria-label, which on the list
  // would read "Mapping warnings" instead of the warnings.
  function expectLinked(name: string, warning: RegExp, controls: number) {
    const row = rowGroup(name)
    const list = ownWarningList(row)
    const items = within(list).getAllByRole('listitem')
    const own = ownControls(row)
    expect(own).toHaveLength(controls)
    for (const el of own) {
      const described = describedElements(el)
      expect(described).toEqual(items)
      for (const d of described!) {
        expect(rowOf(d!)).toBe(row)
        expect(d).not.toHaveAttribute('aria-label')
      }
      expect(el).toHaveAccessibleDescription(expect.stringMatching(warning))
    }
  }
  function expectUnlinked(name: string, controls: number) {
    const row = rowGroup(name)
    expect(warningLists(row).filter((l) => rowOf(l) === row)).toHaveLength(0)
    const own = ownControls(row)
    expect(own).toHaveLength(controls)
    for (const el of own) {
      expect(el).not.toHaveAttribute('aria-describedby')
      expect(el).toHaveAccessibleDescription('')
    }
  }

  // Rows with both, one or neither of source and target, an each inside an
  // each, and rows that differ only by position or by parent.
  function namesConfig(): Config {
    return {
      mappings: [
        { type: 'remove', source: 'internal_id' },
        { type: 'each', source: 'items', target: 'lines', mappings: [
          { type: 'rename', source: 'sku', target: 'code' },
          { type: 'each', source: 'parts', target: 'components', mappings: [
            { type: 'copy', source: 'id', target: 'partId' },
          ] },
        ] },
        { type: 'static', source: '', target: 'lineType', value: 'Item' },
        { type: 'rename', source: '', target: '' },
        { type: 'each', source: 'payments', target: 'tenders', mappings: [
          { type: 'rename', source: 'sku', target: 'code' },
        ] },
        { type: 'rename' },
      ],
    }
  }
  // [row name, the row's type]
  const NAMED_ROWS: [string, string][] = [
    ['mapping 1 (internal_id)', 'remove'],
    ['mapping 2 (items → lines)', 'each'],
    ['item mapping 1 of mapping 2 (sku → code)', 'rename'],
    ['item mapping 2 of mapping 2 (parts → components)', 'each'],
    ['item mapping 1 of item mapping 2 of mapping 2 (id → partId)', 'copy'],
    ['mapping 3 (lineType)', 'static'],
    ['mapping 4', 'rename'],
    ['mapping 5 (payments → tenders)', 'each'],
    ['item mapping 1 of mapping 5 (sku → code)', 'rename'],
    ['mapping 6', 'rename'],
  ]

  describe('criterion 1: select and button names', () => {
    it('names every row by position and what it maps, nested rows by their parent', () => {
      renderEditor(namesConfig())
      expect(screen.getAllByRole('combobox')).toHaveLength(NAMED_ROWS.length)
      for (const [name, type] of NAMED_ROWS) {
        const select = screen.getByRole('combobox', { name: `Type of ${name}` })
        expect(select).toHaveValue(type)
        const remove = screen.getByRole('button', { name: `Remove ${name}` })
        // Both sit in the same row.
        expect(rowOf(select)).not.toBeNull()
        expect(rowOf(select)).toBe(rowOf(remove))
      }
    })

    it('uses the exact names from the issue', () => {
      renderEditor({ mappings: [
        { type: 'remove', source: 'internal_id' },
        { type: 'each', source: 'items', target: 'lines', mappings: [{ type: 'rename', source: 'sku', target: 'code' }] },
      ] })
      expect(screen.getByRole('combobox', { name: 'Type of mapping 2 (items → lines)' })).toHaveValue('each')
      expect(screen.getByRole('button', { name: 'Remove mapping 2 (items → lines)' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Type of item mapping 1 of mapping 2 (sku → code)' })).toHaveValue('rename')
      expect(screen.getByRole('button', { name: 'Remove mapping 1 (internal_id)' })).toBeInTheDocument()
      // The old shared names are gone.
      expect(screen.queryByRole('combobox', { name: 'Mapping type' })).toBeNull()
      expect(screen.queryByRole('button', { name: 'Remove mapping' })).toBeNull()
    })

    it('a row with only a source, only a target or neither', () => {
      renderEditor({ mappings: [
        { type: 'remove', source: 'internal_id' },
        { type: 'static', source: '', target: 'lineType', value: 'Item' },
        { type: 'rename', source: '', target: '' },
        { type: 'copy' },
      ] })
      expect(screen.getByRole('button', { name: 'Remove mapping 1 (internal_id)' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Type of mapping 1 (internal_id)' })).toHaveValue('remove')
      expect(screen.getByRole('button', { name: 'Remove mapping 2 (lineType)' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Type of mapping 2 (lineType)' })).toHaveValue('static')
      expect(screen.getByRole('button', { name: 'Remove mapping 3' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Type of mapping 3' })).toHaveValue('rename')
      expect(screen.getByRole('button', { name: 'Remove mapping 4' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Type of mapping 4' })).toHaveValue('copy')
    })

    it.each<[string, () => Config]>([
      ['names config', namesConfig],
      ['POS template config', templateConfig],
    ])('every select and row button name is unique (%s)', (_name, config) => {
      renderEditor(config())
      const named = [...screen.getAllByRole('combobox'), ...screen.getAllByRole('button')]
      const names = named.map((el) => {
        const name = el.getAttribute('aria-label') ?? el.textContent ?? ''
        expect(el).toHaveAccessibleName(name)
        return name
      })
      // The add buttons repeat on purpose; the per-row names must not.
      const perRow = names.filter((n) => !n.startsWith('+ Add'))
      expect(perRow.length).toBeGreaterThan(0)
      expect(new Set(perRow).size).toBe(perRow.length)
      // A select and a remove button per row, nothing else.
      expect(perRow).toHaveLength(2 * screen.getAllByRole('combobox').length)
    })

    it('a remove button named after a row removes that row', () => {
      const { last } = renderEditor(namesConfig())
      fireEvent.click(screen.getByRole('button', { name: 'Remove item mapping 1 of mapping 5 (sku → code)' }))
      expect(mappingsOf(last())[4].mappings).toEqual([])
      // The row with the same source and target under mapping 2 stays.
      expect(mappingsOf(last())[1]).toEqual(mappingsOf(namesConfig())[1])
      fireEvent.click(screen.getByRole('button', { name: 'Remove item mapping 1 of item mapping 2 of mapping 2 (id → partId)' }))
      expect(((mappingsOf(last())[1].mappings as Mapping[])[1]).mappings).toEqual([])
    })
  })

  describe('criterion 2: row groups', () => {
    it('each row is a group named after it, holding its own select and button', () => {
      renderEditor(namesConfig())
      for (const [name] of NAMED_ROWS) {
        const row = rowGroup(name)
        expect(rowOf(within(row).getByRole('combobox', { name: `Type of ${name}` }))).toBe(row)
        expect(rowOf(within(row).getByRole('button', { name: `Remove ${name}` }))).toBe(row)
      }
      expect(rowGroup('mapping 2 (items → lines)')).toHaveAccessibleName('Mapping 2 (items → lines)')
      expect(rowGroup('item mapping 1 of mapping 2 (sku → code)'))
        .toHaveAccessibleName('Item mapping 1 of mapping 2 (sku → code)')
      expect(rowGroup('mapping 4')).toHaveAccessibleName('Mapping 4')
    })

    it('the repeated Source Field and Target Field inputs sit in their row group', () => {
      renderEditor(namesConfig())
      const fields = (name: string) => {
        const row = rowGroup(name)
        const own = (label: string) => within(row).getAllByLabelText(label).filter((el) => rowOf(el) === row)
        return [own('Source Field'), own('Target Field')]
      }
      const [src1, tgt1] = fields('item mapping 1 of mapping 2 (sku → code)')
      expect(src1).toHaveLength(1)
      expect(src1[0]).toHaveValue('sku')
      expect(tgt1[0]).toHaveValue('code')
      const [src5, tgt5] = fields('item mapping 1 of mapping 5 (sku → code)')
      expect(src5).toHaveLength(1)
      expect(src5[0]).not.toBe(src1[0])
      expect(tgt5[0]).not.toBe(tgt1[0])
      // Every Source Field sits in a row group whose name holds its value.
      for (const input of screen.getAllByLabelText('Source Field') as HTMLInputElement[]) {
        const row = rowOf(input)
        expect(row).not.toBeNull()
        expect(row!.getAttribute('aria-label')).toMatch(/^(Item mapping|Mapping) \d/)
        if (input.value) expect(row!.getAttribute('aria-label')).toContain(input.value)
      }
    })
  })

  describe('criterion 3: warnings linked with aria-describedby', () => {
    it("links every warned row's controls to its own list, and leaves clean rows unlinked", () => {
      renderEditor({ mappings: [
        { type: 'rename', target: 'b' },
        { type: 'copy', source: 'a', target: 'b' },
        { type: 'template', target: 't' },
        { type: 'each', source: 'items', target: 'lines', drop_unmapped: true, mappings: [] },
        { type: 'static', target: 'x', value: 'y' },
        { type: 'remove', source: 'a.b' },
        { type: 'static', value: 'v' },
      ] })
      expectLinked('mapping 1 (b)', /Source is required/, 3)
      expectUnlinked('mapping 2 (a → b)', 3)
      expectLinked('mapping 3 (t)', /Template is required/, 3)
      // select, source array, target and the each row's drop-unmapped checkbox
      expectLinked('mapping 4 (items → lines)', /"Drop unmapped item fields" is on/, 4)
      expect(screen.getByLabelText('Drop unmapped item fields'))
        .toHaveAccessibleDescription(expect.stringMatching(/empty object/))
      expectUnlinked('mapping 5 (x)', 3)
      expectLinked('mapping 6 (a.b)', /does not follow dotted paths/, 2)
      expectLinked('mapping 7', /Target is required/, 3)
    })

    it("a row's controls read only its own warnings, never another row's", () => {
      renderEditor({ mappings: [
        { type: 'rename', target: 'b' },
        { type: 'copy', source: 'a' },
      ] })
      const second = screen.getByRole('combobox', { name: 'Type of mapping 2 (a)' })
      expect(second).toHaveAccessibleDescription(expect.stringMatching(/Target is required/))
      expect(second).not.toHaveAccessibleDescription(expect.stringMatching(/Source is required/))
      const first = screen.getByRole('combobox', { name: 'Type of mapping 1 (b)' })
      expect(first).toHaveAccessibleDescription(expect.stringMatching(/Source is required/))
      expect(first).not.toHaveAccessibleDescription(expect.stringMatching(/Target is required/))
      // Each row's warnings have ids of their own.
      const ids = warningLists().flatMap((l) => within(l).getAllByRole('listitem').map((li) => li.id))
      expect(ids).toHaveLength(2)
      expect(ids.every((id) => id !== '')).toBe(true)
      expect(new Set(ids).size).toBe(2)
    })

    it('links nested rows to their own list, apart from the each row around them', () => {
      renderEditor({ mappings: [
        { type: 'each', source: 'items', mappings: [
          { type: 'rename', target: 'qty' },
          { type: 'each', source: 'parts', target: 'comps', mappings: [
            { type: 'copy', source: 'id' },
            { type: 'copy', source: 'n', target: 'name' },
          ] },
          { type: 'rename', source: 'sku', target: 'code' },
        ] },
      ] })
      // The each row warns about its target; its checkbox and fields read that,
      // not its first item's missing source.
      expectLinked('mapping 1 (items)', /Target is required/, 4)
      expect(screen.getByRole('combobox', { name: 'Type of mapping 1 (items)' }))
        .not.toHaveAccessibleDescription(expect.stringMatching(/Source is required/))
      expectLinked('item mapping 1 of mapping 1 (qty)', /Source is required/, 3)
      // The nested each is clean, its own checkbox included.
      expectUnlinked('item mapping 2 of mapping 1 (parts → comps)', 4)
      // An item two levels down warns about its target; its sibling is clean.
      expectLinked('item mapping 1 of item mapping 2 of mapping 1 (id)', /Target is required/, 3)
      expectUnlinked('item mapping 2 of item mapping 2 of mapping 1 (n → name)', 3)
      expectUnlinked('item mapping 3 of mapping 1 (sku → code)', 3)
    })

    it('fixing the last warning removes the link, causing one adds it', () => {
      renderEditor({ mappings: [{ type: 'rename', source: 'a', target: 'b' }] })
      expectUnlinked('mapping 1 (a → b)', 3)
      fireEvent.change(screen.getByLabelText('Target Field'), { target: { value: '' } })
      expectLinked('mapping 1 (a)', /Target is required/, 3)
      fireEvent.change(screen.getByLabelText('Source Field'), { target: { value: '' } })
      expectLinked('mapping 1', /Source is required/, 3)
      expectLinked('mapping 1', /Target is required/, 3)
      fireEvent.change(screen.getByLabelText('Source Field'), { target: { value: 'a' } })
      expectLinked('mapping 1 (a)', /Target is required/, 3)
      fireEvent.change(screen.getByLabelText('Target Field'), { target: { value: 'c' } })
      expectUnlinked('mapping 1 (a → c)', 3)
      // A type change can cause one too: a template needs its template.
      fireEvent.change(screen.getByRole('combobox', { name: 'Type of mapping 1 (a → c)' }), { target: { value: 'template' } })
      expectLinked('mapping 1 (a → c)', /Template is required/, 3)
      expect(screen.getByLabelText('Template')).toHaveAccessibleDescription(expect.stringMatching(/Template is required/))
      fireEvent.change(screen.getByLabelText('Template'), { target: { value: '{x}' } })
      expectUnlinked('mapping 1 (a → c)', 3)
    })

    it("an each row's drop-unmapped checkbox gains and loses the link with the warning", () => {
      renderEditor({ mappings: [{ type: 'each', source: 'items', target: 'lines', mappings: [] }] })
      expectUnlinked('mapping 1 (items → lines)', 4)
      fireEvent.click(screen.getByLabelText('Drop unmapped item fields'))
      expectLinked('mapping 1 (items → lines)', /"Drop unmapped item fields" is on/, 4)
      // A new item mapping clears the each row's warning but has its own.
      fireEvent.click(screen.getByRole('button', { name: '+ Add Item Mapping' }))
      expectUnlinked('mapping 1 (items → lines)', 4)
      expectLinked('item mapping 1 of mapping 1', /Source is required/, 3)
      fireEvent.change(within(rowGroup('item mapping 1 of mapping 1')).getByLabelText('Source Field'), { target: { value: 'sku' } })
      fireEvent.change(within(rowGroup('item mapping 1 of mapping 1 (sku)')).getByLabelText('Target Field'), { target: { value: 'code' } })
      expectUnlinked('item mapping 1 of mapping 1 (sku → code)', 3)
      expect(document.querySelectorAll('[aria-describedby]')).toHaveLength(0)
    })
  })

  describe('criterion 4: names follow edits and removals', () => {
    it('typing a source or target updates the names', () => {
      renderEditor({ mappings: [] })
      fireEvent.click(screen.getByRole('button', { name: '+ Add Mapping' }))
      expect(screen.getByRole('combobox', { name: 'Type of mapping 1' })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Remove mapping 1' })).toBeInTheDocument()
      expect(rowGroup('mapping 1')).toBeInTheDocument()

      fireEvent.change(screen.getByLabelText('Source Field'), { target: { value: 'items' } })
      expect(screen.getByRole('button', { name: 'Remove mapping 1 (items)' })).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Remove mapping 1' })).toBeNull()

      fireEvent.change(screen.getByLabelText('Target Field'), { target: { value: 'lines' } })
      expect(screen.getByRole('combobox', { name: 'Type of mapping 1 (items → lines)' })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Remove mapping 1 (items → lines)' })).toBeInTheDocument()
      expect(rowGroup('mapping 1 (items → lines)')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Remove mapping 1 (items)' })).toBeNull()

      fireEvent.change(screen.getByLabelText('Source Field'), { target: { value: '' } })
      expect(screen.getByRole('button', { name: 'Remove mapping 1 (lines)' })).toBeInTheDocument()

      // An item mapping's names follow its own edits.
      fireEvent.change(screen.getByLabelText('Source Field'), { target: { value: 'items' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Type of mapping 1 (items → lines)' }), { target: { value: 'each' } })
      fireEvent.click(screen.getByRole('button', { name: '+ Add Item Mapping' }))
      expect(screen.getByRole('combobox', { name: 'Type of item mapping 1 of mapping 1' })).toBeInTheDocument()
      fireEvent.change(within(rowGroup('item mapping 1 of mapping 1')).getByLabelText('Target Field'), { target: { value: 'code' } })
      expect(screen.getByRole('button', { name: 'Remove item mapping 1 of mapping 1 (code)' })).toBeInTheDocument()
    })

    it('removing a row renumbers the rows after it, nested ones included', () => {
      const { last } = renderEditor({ mappings: [
        { type: 'rename', source: 'a', target: 'b' },
        { type: 'each', source: 'items', target: 'lines', mappings: [
          { type: 'rename', source: 'sku', target: 'code' },
          { type: 'rename', source: 'qty', target: 'quantity' },
        ] },
        { type: 'rename', source: 'c', target: 'd' },
      ] })
      fireEvent.click(screen.getByRole('button', { name: 'Remove mapping 1 (a → b)' }))
      expect(mappingsOf(last()).map((m) => m.source)).toEqual(['items', 'c'])
      expect(screen.getByRole('button', { name: 'Remove mapping 1 (items → lines)' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Type of item mapping 2 of mapping 1 (qty → quantity)' })).toHaveValue('rename')
      expect(screen.getByRole('button', { name: 'Remove mapping 2 (c → d)' })).toBeInTheDocument()
      expect(rowGroup('mapping 2 (c → d)')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /mapping 3/ })).toBeNull()
      expect(screen.queryByRole('group', { name: /of mapping 2/ })).toBeNull()

      fireEvent.click(screen.getByRole('button', { name: 'Remove item mapping 1 of mapping 1 (sku → code)' }))
      expect(mappingsOf(last())[0].mappings).toEqual([{ type: 'rename', source: 'qty', target: 'quantity' }])
      expect(screen.getByRole('button', { name: 'Remove item mapping 1 of mapping 1 (qty → quantity)' })).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /item mapping 2/ })).toBeNull()
    })
  })
})
