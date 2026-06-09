// SchemaTree renders a source field tree (#81): types as colored badges,
// expand/collapse, search, and a per-field "pick" action. It's source-agnostic
// — it consumes SchemaField[] produced by inferSchema (JSON sources) or the DB
// information_schema endpoint. PR2 layers drag-and-drop via the renderField slot
// without touching this component.
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import type { SchemaField, SchemaFieldType } from './schema'

// seedExpanded returns the set of paths to expand by default (top two levels).
function seedExpanded(fields: SchemaField[]): Set<string> {
  const s = new Set<string>()
  const walk = (fs: SchemaField[], depth: number) => {
    for (const f of fs) {
      if (f.children && depth < 1) {
        s.add(f.path)
        walk(f.children, depth + 1)
      }
    }
  }
  walk(fields, 0)
  return s
}

const BADGE: Record<SchemaFieldType, { bg: string; fg: string }> = {
  string: { bg: '#e0e7ff', fg: '#3730a3' },
  number: { bg: '#dcfce7', fg: '#166534' },
  boolean: { bg: '#f3e8ff', fg: '#6b21a8' },
  object: { bg: '#fef3c7', fg: '#92400e' },
  array: { bg: '#fed7aa', fg: '#9a3412' },
  null: { bg: '#f1f5f9', fg: '#64748b' },
  unknown: { bg: '#f1f5f9', fg: '#64748b' },
}

function TypeBadge({ type }: { type: SchemaFieldType }) {
  const c = BADGE[type]
  return (
    <span style={{ fontSize: '10px', fontWeight: 600, color: c.fg, background: c.bg, borderRadius: '3px', padding: '1px 5px', fontFamily: 'monospace' }}>
      {type}
    </span>
  )
}

// matches returns true if the field or any descendant path/name contains q.
function matches(f: SchemaField, q: string): boolean {
  if (!q) return true
  if (f.path.toLowerCase().includes(q) || f.name.toLowerCase().includes(q)) return true
  return (f.children || []).some((c) => matches(c, q))
}

export function SchemaTree({
  fields,
  onPick,
  renderField,
  emptyHint,
}: {
  fields: SchemaField[]
  /** Called when the user clicks the pick (+) action on a field. */
  onPick?: (field: SchemaField) => void
  /** Optional wrapper around a field's row (e.g. to make it draggable in PR2). */
  renderField?: (field: SchemaField, defaultRow: ReactNode) => ReactNode
  emptyHint?: string
}) {
  const [search, setSearch] = useState('')
  // Default-expand the top two levels so the tree is immediately useful, and
  // re-seed whenever a fresh schema is discovered (the fields prop changes).
  const [expanded, setExpanded] = useState<Set<string>>(() => seedExpanded(fields))
  useEffect(() => {
    setExpanded(seedExpanded(fields))
  }, [fields])
  const q = search.trim().toLowerCase()

  const toggle = (path: string) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })

  const visible = useMemo(() => fields.filter((f) => matches(f, q)), [fields, q])

  const renderNode = (field: SchemaField, depth: number): ReactNode => {
    if (q && !matches(field, q)) return null
    const hasChildren = !!field.children && field.children.length > 0
    const isOpen = q ? true : expanded.has(field.path)

    const row = (
      <div style={{ display: 'flex', alignItems: 'center', gap: '6px', padding: '2px 0', paddingLeft: `${depth * 14}px` }}>
        {hasChildren ? (
          <button
            onClick={() => toggle(field.path)}
            style={{ border: 'none', background: 'none', cursor: 'pointer', color: '#64748b', width: '12px', padding: 0 }}
          >
            {isOpen ? '▾' : '▸'}
          </button>
        ) : (
          <span style={{ width: '12px' }} />
        )}
        <span style={{ fontFamily: 'monospace', fontSize: '12px', color: '#111827' }}>{field.name}</span>
        <TypeBadge type={field.type} />
        {field.nullable && <span style={{ fontSize: '10px', color: '#94a3b8' }}>nullable</span>}
        {onPick && (
          <button
            title="Add as mapping"
            onClick={() => onPick(field)}
            style={{ marginLeft: 'auto', border: '1px solid #cbd5e1', background: '#f8fafc', borderRadius: '3px', cursor: 'pointer', fontSize: '11px', color: '#334155', padding: '0 6px' }}
          >
            +
          </button>
        )}
      </div>
    )

    return (
      <div key={field.path}>
        {renderField ? renderField(field, row) : row}
        {hasChildren && isOpen && (
          <div style={{ borderLeft: '1px solid #e2e8f0', marginLeft: `${depth * 14 + 5}px` }}>
            {field.children!.map((c) => renderNode(c, depth + 1))}
          </div>
        )}
      </div>
    )
  }

  if (fields.length === 0) {
    return <div style={{ fontSize: '12px', color: '#94a3b8', padding: '6px' }}>{emptyHint || 'No fields discovered.'}</div>
  }

  return (
    <div>
      <input
        type="text"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        placeholder="Search fields…"
        style={{ width: '100%', padding: '4px 8px', border: '1px solid #d1d5db', borderRadius: '4px', fontSize: '12px', marginBottom: '6px', boxSizing: 'border-box' }}
      />
      <div style={{ maxHeight: '260px', overflowY: 'auto', border: '1px solid #e2e8f0', borderRadius: '4px', padding: '4px 6px', background: '#fff' }}>
        {visible.map((f) => renderNode(f, 0))}
      </div>
    </div>
  )
}
