import { describe, it, expect } from 'vitest'
import { projectSchemaThroughFilter, type SchemaField } from './schema'

// Source schema for a sales order with a nested customer object and a lines[].
const source: SchemaField[] = [
  { path: 'order_id', name: 'order_id', type: 'string' },
  { path: 'total', name: 'total', type: 'number' },
  {
    path: 'customer', name: 'customer', type: 'object', children: [
      { path: 'customer.name', name: 'name', type: 'string' },
      { path: 'customer.email', name: 'email', type: 'string' },
    ],
  },
  { path: 'lines', name: 'lines', type: 'array' },
]

describe('projectSchemaThroughFilter', () => {
  it('returns the source unchanged when the filter only has row rules', () => {
    expect(projectSchemaThroughFilter(source, { rules: [{ field: 'total', operator: 'gt', value: '0' }] })).toEqual(source)
    expect(projectSchemaThroughFilter(source, {})).toEqual(source)
    expect(projectSchemaThroughFilter(source, undefined)).toEqual(source)
  })

  it('prunes to extract_fields, preserving nesting and dropping the rest', () => {
    const out = projectSchemaThroughFilter(source, { extract_fields: ['order_id', 'customer.email'] })
    // order_id kept; total dropped; customer kept but only with email (name dropped); lines dropped.
    expect(out.map((f) => f.path)).toEqual(['order_id', 'customer'])
    const customer = out.find((f) => f.path === 'customer')
    expect(customer?.children?.map((c) => c.name)).toEqual(['email'])
  })

  it('keeps the whole subtree when an object path is extracted', () => {
    const out = projectSchemaThroughFilter(source, { extract_fields: ['customer'] })
    const customer = out.find((f) => f.path === 'customer')
    expect(customer?.children?.map((c) => c.name)).toEqual(['name', 'email'])
  })

  it('emits only the mapped destination names (flat) when flattening', () => {
    const out = projectSchemaThroughFilter(source, {
      flatten_path: 'lines',
      flatten_fields: { sku: 'sku', qty: 'quantity' },
      flatten_include: { order_id: 'order_id', 'customer.email': 'email' },
    })
    expect(out.map((f) => ({ name: f.name, path: f.path }))).toEqual([
      { name: 'sku', path: 'sku' },
      { name: 'quantity', path: 'quantity' },
      { name: 'order_id', path: 'order_id' },
      { name: 'email', path: 'email' },
    ])
  })

  it('resolves flattened field types from the source where the path matches', () => {
    const out = projectSchemaThroughFilter(source, {
      flatten_path: 'lines',
      flatten_include: { 'customer.email': 'email', order_id: 'order_id' },
    })
    expect(out.find((f) => f.name === 'email')?.type).toBe('string')
    expect(out.find((f) => f.name === 'order_id')?.type).toBe('string')
  })
})
