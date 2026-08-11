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

// --- Filter projection (mirror src/cmd/data-filter/main.go) ---------------
// When a Filter node sits between a source and a Converter and it *projects*
// fields (extract_fields, or flatten_fields/flatten_include under a
// flatten_path), the Converter downstream only receives those fields — not the
// full source. These helpers transform a discovered source schema into the
// schema the Filter actually emits, so "Discover schema" doesn't offer fields
// the user filtered out.

// pruneToPaths keeps only the fields on/under/above the extracted paths,
// preserving nesting (extract "customer.email" keeps customer → email; extract
// "customer" keeps the whole customer subtree). Mirrors extractFields in Go.
function pruneToPaths(fields: SchemaField[], keep: string[]): SchemaField[] {
  const out: SchemaField[] = []
  for (const f of fields) {
    const isAncestorOrSelf = keep.some((p) => p === f.path || p.startsWith(f.path + '.'))
    const isDescendant = keep.some((p) => f.path === p || f.path.startsWith(p + '.'))
    if (!isAncestorOrSelf && !isDescendant) continue
    let children = f.children
    if (children) {
      // A field at/under an extracted path keeps all its children; an ancestor
      // is pruned further so only the branch leading to the kept leaf survives.
      children = isDescendant ? children : pruneToPaths(children, keep)
    }
    out.push({ ...f, children })
  }
  return out
}

// projectSchemaThroughFilter returns the schema a Filter emits given the source
// schema. No projection configured (row rules only) → source is unchanged.
export function projectSchemaThroughFilter(
  sourceFields: SchemaField[],
  cfg: Record<string, unknown> | undefined,
): SchemaField[] {
  if (!cfg) return sourceFields
  const flattenPath = (cfg.flatten_path as string) || ''
  const flattenFields = (cfg.flatten_fields as Record<string, string>) || {}
  const flattenInclude = (cfg.flatten_include as Record<string, string>) || {}
  const extract = (cfg.extract_fields as string[]) || []

  // Flatten: output rows carry only the mapped destination names (flat).
  if (flattenPath && (Object.keys(flattenFields).length > 0 || Object.keys(flattenInclude).length > 0)) {
    const typeByPath = new Map<string, SchemaFieldType>()
    const walk = (fs: SchemaField[]) => { for (const f of fs) { typeByPath.set(f.path, f.type); if (f.children) walk(f.children) } }
    walk(sourceFields)
    const resolveType = (srcPath: string): SchemaFieldType => {
      if (typeByPath.has(srcPath)) return typeByPath.get(srcPath) as SchemaFieldType
      for (const [p, t] of typeByPath) { if (p === srcPath || p.endsWith('.' + srcPath)) return t }
      return 'unknown'
    }
    const seen = new Set<string>()
    const out: SchemaField[] = []
    const add = (srcPath: string, destName: string) => {
      if (!destName || seen.has(destName)) return
      seen.add(destName)
      out.push({ path: destName, name: destName, type: resolveType(srcPath) })
    }
    for (const [src, dest] of Object.entries(flattenFields)) add(src, dest)
    for (const [src, dest] of Object.entries(flattenInclude)) add(src, dest)
    return out
  }

  // Extract only: prune the source tree to the kept paths.
  if (extract.length > 0) {
    return pruneToPaths(sourceFields, extract)
  }

  return sourceFields
}
