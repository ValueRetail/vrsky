// Schema model + discovery helpers for the visual field-mapping UI (#81).
//
// A SchemaField is one node in the source field tree. Types come from the DB
// information_schema (authoritative) or are inferred client-side from a fetched
// JSON sample for the other source types.

export type SchemaFieldType =
  | 'string'
  | 'number'
  | 'boolean'
  | 'object'
  | 'array'
  | 'null'
  | 'unknown'

export interface SchemaField {
  /** Dot-path from the record root, e.g. "customer.email" (matches the converter's getNestedField). */
  path: string
  /** Leaf label (last path segment). */
  name: string
  type: SchemaFieldType
  nullable?: boolean
  /** Present for object fields. */
  children?: SchemaField[]
}

export function jsTypeOf(v: unknown): SchemaFieldType {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  switch (typeof v) {
    case 'string':
      return 'string'
    case 'number':
      return 'number'
    case 'boolean':
      return 'boolean'
    case 'object':
      return 'object'
    default:
      return 'unknown'
  }
}

// Coarse bucket for a Salesforce describe field type → drives the badge.
export function sfTypeToBadge(sfType: string): SchemaFieldType {
  switch (sfType.toLowerCase()) {
    case 'int':
    case 'integer':
    case 'long':
    case 'double':
    case 'currency':
    case 'percent':
      return 'number'
    case 'boolean':
      return 'boolean'
    default:
      // string, textarea, picklist, reference, id, date, datetime, email,
      // phone, url, address, base64, … all render as string-ish.
      return 'string'
  }
}

// Coarse bucket for a SQL data_type (information_schema) → drives the badge.
export function sqlTypeToBadge(sqlType: string): SchemaFieldType {
  const t = sqlType.toLowerCase()
  if (/(int|serial|numeric|decimal|real|double|float|money)/.test(t)) return 'number'
  if (/bool/.test(t)) return 'boolean'
  if (/(json|jsonb)/.test(t)) return 'object'
  if (/array/.test(t)) return 'array'
  return 'string' // text, varchar, char, timestamp, date, uuid, … render as string-ish
}

// inferSchema builds a field tree from a fetched sample. Arrays (e.g. DB rows)
// are represented by their first element, since the converter operates per
// record. Nested objects recurse (dot-path); arrays are leaves (the converter
// can't index into arrays — the filter's "split into rows" handles those).
export function inferSchema(sample: unknown): SchemaField[] {
  let root = sample
  if (Array.isArray(root)) root = root[0]
  if (root === null || root === undefined || typeof root !== 'object' || Array.isArray(root)) {
    return []
  }
  return fieldsOf(root as Record<string, unknown>, '')
}

function fieldsOf(obj: Record<string, unknown>, prefix: string): SchemaField[] {
  return Object.entries(obj).map(([key, val]) => {
    const path = prefix ? `${prefix}.${key}` : key
    const type = jsTypeOf(val)
    const field: SchemaField = { path, name: key, type }
    if (type === 'object' && val) {
      field.children = fieldsOf(val as Record<string, unknown>, path)
    }
    return field
  })
}

// fieldsFromColumns builds a flat schema from a list of column descriptors
// (DB information_schema, or CSV headers).
export function fieldsFromColumns(
  columns: Array<{ name: string; type?: SchemaFieldType; nullable?: boolean }>,
): SchemaField[] {
  return columns.map((c) => ({ path: c.name, name: c.name, type: c.type ?? 'string', nullable: c.nullable }))
}
