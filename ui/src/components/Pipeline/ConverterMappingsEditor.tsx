import type { ReactNode } from 'react'
import {
  CONVERTER_MAPPING_LABELS,
  CONVERTER_MAPPING_TYPES,
  converterMappingWarnings,
  type ConverterMapping,
} from '@/types/nodeConfig'
import { StyledInput } from './StyledFields'

// The field-mapping part of the converter editor (#458), moved out of
// PropertyEditor.tsx so it can be tested on its own. It knows every mapping
// type the data-converter runs, `each` with its nested item mappings
// included, and the node's `split_records`.
//
// Every write spreads what was there (`{...config, …}`, `{...mapping, …}`),
// so keys this editor does not render survive a save, and a row switched
// from `each` to another type and back still has its item mappings.

// Wrap a top-level row's own fields (not an `each` row's item mappings) or
// the add button; PropertyEditor passes its drag-and-drop drop targets here.
// Nested rows are not wrapped.
type RowWrap = (index: number, row: ReactNode) => ReactNode
type AddWrap = (button: ReactNode) => ReactNode

const selectStyle = {
  fontSize: '11px', padding: '3px 6px', border: '1px solid #d1d5db', borderRadius: '4px', backgroundColor: '#fff',
} as const
const checkboxLabelStyle = {
  display: 'flex', alignItems: 'center', gap: '6px', fontSize: '12px', color: '#374151', marginTop: '8px',
} as const

function MappingRow({
  mapping,
  onChange,
  onRemove,
  depth,
  wrapFields,
}: {
  mapping: ConverterMapping
  onChange: (patch: Partial<ConverterMapping>) => void
  onRemove: () => void
  depth: number
  // Wraps the row's own fields only. The item mappings of an `each` sit
  // outside it, so a schema field dropped on an item never lands on the
  // `each` row's source.
  wrapFields: (fields: ReactNode) => ReactNode
}) {
  // The converter treats an empty type as rename.
  const type = mapping.type || 'rename'
  const known = CONVERTER_MAPPING_TYPES.includes(type)
  const warnings = converterMappingWarnings(mapping)

  return (
    <div style={{ backgroundColor: depth > 0 ? '#ffffff' : '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '6px', padding: '8px', marginBottom: '6px' }}>
      {wrapFields(<>
      <div style={{ display: 'flex', alignItems: 'center', gap: '4px', marginBottom: '6px' }}>
        <select
          aria-label="Mapping type"
          value={type}
          onChange={(e) => onChange({ type: e.target.value })}
          style={selectStyle}
        >
          {CONVERTER_MAPPING_TYPES.map((t) => (
            <option key={t} value={t}>{CONVERTER_MAPPING_LABELS[t] ?? t}</option>
          ))}
          {/* A type the converter does not run is shown as itself, not as
              the first option, so the user sees what is stored. */}
          {!known && <option value={type}>{`${type} (unknown)`}</option>}
        </select>
        <div style={{ flex: 1 }} />
        <button
          type="button"
          aria-label="Remove mapping"
          onClick={onRemove}
          style={{ background: 'none', border: 'none', color: '#dc2626', cursor: 'pointer', fontSize: '14px', padding: '0 4px' }}
        >
          ×
        </button>
      </div>
      {type !== 'static' && type !== 'template' && (
        <StyledInput
          label={type === 'each' ? 'Source Array' : 'Source Field'}
          placeholder={type === 'each' ? 'e.g. items' : type === 'remove' ? 'e.g. internal_id' : 'e.g. name or user.email'}
          value={mapping.source ?? ''}
          onChange={(v) => onChange({ source: v })}
        />
      )}
      {type !== 'remove' && (
        <StyledInput
          label="Target Field"
          placeholder={type === 'each' ? 'e.g. salesInvoiceLines' : 'e.g. full_name'}
          value={mapping.target ?? ''}
          onChange={(v) => onChange({ target: v })}
        />
      )}
      {type === 'static' && (
        <StyledInput
          label="Value"
          placeholder="e.g. default_value"
          value={mapping.value == null ? '' : String(mapping.value)}
          onChange={(v) => onChange({ value: v })}
        />
      )}
      {type === 'template' && (
        <StyledInput
          label="Template"
          placeholder="e.g. {first_name} {last_name}"
          value={mapping.expression ?? ''}
          onChange={(v) => onChange({ expression: v })}
        />
      )}
      </>)}
      {type === 'each' && (
        <div role="group" aria-label="Item mappings" style={{ borderLeft: '2px solid #e5e7eb', paddingLeft: '8px' }}>
          <div style={{ fontSize: '11px', color: '#6b7280', marginBottom: '4px' }}>
            Applied to every object in the source array; the mapped array is written under the target name.
            Items that are not objects are kept as they are. With a dotted source and node-level &quot;Drop unmapped
            fields&quot; off, the original array stays in place as well.
          </div>
          <MappingList
            mappings={mapping.mappings ?? []}
            onChange={(next) => onChange({ mappings: next })}
            depth={depth + 1}
            addLabel="+ Add Item Mapping"
          />
          <label style={checkboxLabelStyle}>
            <input
              type="checkbox"
              checked={mapping.drop_unmapped ?? false}
              onChange={(e) => onChange({ drop_unmapped: e.target.checked })}
            />
            Drop unmapped item fields
          </label>
        </div>
      )}
      {warnings.length > 0 && (
        <ul aria-label="Mapping warnings" style={{ margin: '6px 0 0', paddingLeft: '16px', fontSize: '11px', color: '#b45309' }}>
          {warnings.map((w) => <li key={w}>{w}</li>)}
        </ul>
      )}
    </div>
  )
}

export function MappingList({
  mappings,
  onChange,
  depth = 0,
  addLabel = '+ Add Mapping',
  wrapRow = (_i, row) => row,
  wrapAdd = (button) => button,
}: {
  mappings: ConverterMapping[]
  onChange: (mappings: ConverterMapping[]) => void
  depth?: number
  addLabel?: string
  wrapRow?: RowWrap
  wrapAdd?: AddWrap
}) {
  const update = (index: number, patch: Partial<ConverterMapping>) =>
    onChange(mappings.map((m, i) => (i === index ? { ...m, ...patch } : m)))

  return (
    <>
      {mappings.map((m, i) => (
        <MappingRow
          key={i}
          mapping={m}
          onChange={(patch) => update(i, patch)}
          onRemove={() => onChange(mappings.filter((_, j) => j !== i))}
          depth={depth}
          wrapFields={(fields) => wrapRow(i, fields)}
        />
      ))}
      {wrapAdd(
        <button
          type="button"
          onClick={() => onChange([...mappings, { source: '', target: '', type: 'rename' }])}
          style={{ width: '100%', padding: '6px 12px', fontSize: '12px', fontWeight: 600, backgroundColor: '#f3f4f6', color: '#374151', border: '1px dashed #d1d5db', borderRadius: '6px', cursor: 'pointer' }}
        >
          {addLabel}
        </button>,
      )}
    </>
  )
}

export default function ConverterMappingsEditor({
  config,
  setConfig,
  wrapRow,
  wrapAdd,
}: {
  config: Record<string, unknown>
  setConfig: (config: Record<string, unknown>) => void
  wrapRow?: RowWrap
  wrapAdd?: AddWrap
}) {
  const mappings = (config.mappings as ConverterMapping[] | undefined) ?? []
  const dropUnmapped = (config.drop_unmapped as boolean | undefined) ?? false
  const splitRecords = (config.split_records as boolean | undefined) ?? false

  return (
    <div style={{ borderTop: '1px solid #e5e7eb', paddingTop: '8px', marginTop: '8px' }}>
      <div style={{ fontSize: '12px', fontWeight: 600, color: '#374151', marginBottom: '4px' }}>
        Field Mappings <span style={{ fontWeight: 400, color: '#9ca3af' }}>(optional, applied before format conversion)</span>
      </div>

      <MappingList
        mappings={mappings}
        onChange={(next) => setConfig({ ...config, mappings: next })}
        wrapRow={wrapRow}
        wrapAdd={wrapAdd}
      />

      <label style={checkboxLabelStyle}>
        <input type="checkbox" checked={dropUnmapped} onChange={(e) => setConfig({ ...config, drop_unmapped: e.target.checked })} />
        Drop unmapped fields
      </label>

      <label style={checkboxLabelStyle}>
        <input type="checkbox" checked={splitRecords} onChange={(e) => setConfig({ ...config, split_records: e.target.checked })} />
        Split records: one message per record
      </label>
      {splitRecords && (
        <div style={{ fontSize: '11px', color: '#6b7280', marginTop: '4px' }}>
          An array result is sent as one message per record; a single record goes out as it is. A payload over the
          converter&apos;s rehydrate cap (128 MB by default) cannot be split and goes to the dead-letter queue. The
          preview below shows the unsplit result.
        </div>
      )}
    </div>
  )
}
