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

const typeSelects = () => screen.getAllByLabelText('Mapping type') as HTMLSelectElement[]
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
    expect(within(group).getAllByLabelText('Mapping type').map((s) => (s as HTMLSelectElement).value))
      .toEqual(['static', 'rename', 'to_number', 'to_number'])
    expect(within(group).getAllByLabelText('Target Field').map((i) => (i as HTMLInputElement).value))
      .toEqual(['lineType', 'lineObjectNumber', 'quantity', 'unitPrice'])
    expect(within(group).getByLabelText('Value')).toHaveValue('Item')
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
    expect(within(itemGroups()[0]).getAllByLabelText('Mapping type')).toHaveLength(5)
    fireEvent.change(within(itemGroups()[0]).getAllByLabelText('Target Field')[4], { target: { value: 'description' } })
    expect((mappingsOf(last())[2].mappings as Mapping[])[4].target).toBe('description')
    // Top-level mappings are untouched.
    expect(mappingsOf(last())).toHaveLength(3)
  })

  it('removes an item mapping', () => {
    const { last } = renderEditor(templateConfig())
    const [group] = itemGroups()
    fireEvent.click(within(group).getAllByRole('button', { name: 'Remove mapping' })[0])
    const original = (templateConfig().mappings as Mapping[])[2].mappings as Mapping[]
    expect(mappingsOf(last())[2].mappings).toEqual(original.slice(1))
    expect(mappingsOf(last())).toHaveLength(3)
    expect(within(itemGroups()[0]).getAllByLabelText('Mapping type')).toHaveLength(3)
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
    fireEvent.change(within(outer).getAllByLabelText('Mapping type')[1], { target: { value: 'each' } })
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
    fireEvent.click(within(itemGroups()[1]).getByRole('button', { name: 'Remove mapping' }))
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
    expect(within(itemGroups()[0]).getAllByLabelText('Mapping type')).toHaveLength(4)
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
    ['a remove with a dotted source', { type: 'remove', source: 'customer.email' }, /top-level|dotted|nested/i],
    ['an each with drop unmapped and no item mappings', { type: 'each', source: 'items', target: 'lines', drop_unmapped: true, mappings: [] }, /drop unmapped|empty/i],
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
    expect(within(drop).getAllByLabelText('Mapping type')).toHaveLength(1)
    expect((within(drop).getByLabelText('Mapping type') as HTMLSelectElement).value).toBe('each')
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
      for (const itemSelect of within(group).getAllByLabelText('Mapping type')) {
        expect(el).not.toContainElement(itemSelect)
      }
    }
    // The other rows' own fields are in their own drop targets.
    expect(within(screen.getByTestId('drop-1')).getByLabelText('Source Field')).toHaveValue('receiptnumber')
    expect(within(screen.getByTestId('drop-0')).getByLabelText('Value')).toHaveValue('')
  })
})
