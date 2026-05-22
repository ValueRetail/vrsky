/**
 * Audit log page — Phase 1G (#72).
 *
 * Read-only timeline of every state-changing request in the current tenant.
 * Filter by action / resource / time window; export filtered set as JSONL.
 */

import { useEffect, useMemo, useState } from 'react'
import { listAudit, auditExportURL, type AuditEntry, type AuditFilters } from '../services/auditService'
import apiClient from '../services/api'

const cell: React.CSSProperties = {
  padding: '8px 10px',
  borderBottom: '1px solid #f3f4f6',
  fontSize: '12px',
  verticalAlign: 'top',
}

const headerCell: React.CSSProperties = { ...cell, fontWeight: 600, background: '#f9fafb', textAlign: 'left' }

const input: React.CSSProperties = {
  padding: '6px 10px',
  border: '1px solid #d1d5db',
  borderRadius: '6px',
  fontSize: '12px',
  minWidth: '160px',
}

function statusBadgeColor(code: number): string {
  if (code >= 500) return '#dc2626'
  if (code >= 400) return '#d97706'
  if (code >= 300) return '#0891b2'
  return '#059669'
}

export default function AuditPage() {
  const [filters, setFilters] = useState<AuditFilters>({})
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize] = useState(50)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = async () => {
    setLoading(true)
    setError(null)
    try {
      const resp = await listAudit(filters, page, pageSize)
      setEntries(resp.data || [])
      setTotal(resp.total || 0)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load audit log')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, filters.action, filters.resource_type, filters.resource_id, filters.user_id, filters.since, filters.until])

  const pages = useMemo(() => Math.max(1, Math.ceil(total / pageSize)), [total, pageSize])

  const handleExport = async () => {
    // We can't just window.open the URL because the API requires the
    // X-Tenant-ID header which is attached by our axios interceptor.
    // Fetch as a blob via apiClient and trigger a save.
    try {
      const resp = await apiClient.get(auditExportURL(filters), { responseType: 'blob' })
      const blob = new Blob([resp.data], { type: 'application/x-ndjson' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `vrsky-audit-${new Date().toISOString().slice(0, 10)}.jsonl`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Export failed')
    }
  }

  const updateFilter = (key: keyof AuditFilters, value: string) => {
    setPage(1)
    setFilters((f) => ({ ...f, [key]: value || undefined }))
  }

  return (
    <div style={{ padding: '20px', maxWidth: '1400px', margin: '0 auto' }}>
      <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '12px' }}>Audit log</h1>
      <p style={{ fontSize: '13px', color: '#6b7280', marginBottom: '20px' }}>
        Immutable record of every state-changing operation in this workspace. Entries persist for 365 days.
      </p>

      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px', marginBottom: '16px', alignItems: 'end' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
          Action
          <input
            style={input}
            placeholder="e.g. connection.create"
            value={filters.action || ''}
            onChange={(e) => updateFilter('action', e.target.value)}
          />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
          Resource type
          <select
            style={input}
            value={filters.resource_type || ''}
            onChange={(e) => updateFilter('resource_type', e.target.value)}
          >
            <option value="">(all)</option>
            <option value="connection">connection</option>
            <option value="secret">secret</option>
            <option value="tenant">tenant</option>
          </select>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
          Resource ID
          <input
            style={input}
            placeholder="UUID"
            value={filters.resource_id || ''}
            onChange={(e) => updateFilter('resource_id', e.target.value)}
          />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
          Since
          <input
            type="datetime-local"
            style={input}
            value={filters.since ? filters.since.slice(0, 16) : ''}
            onChange={(e) => updateFilter('since', e.target.value ? new Date(e.target.value).toISOString() : '')}
          />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: '4px', fontSize: '11px', color: '#374151' }}>
          Until
          <input
            type="datetime-local"
            style={input}
            value={filters.until ? filters.until.slice(0, 16) : ''}
            onChange={(e) => updateFilter('until', e.target.value ? new Date(e.target.value).toISOString() : '')}
          />
        </label>
        <button
          onClick={refresh}
          style={{ padding: '7px 14px', background: '#2563eb', color: '#fff', border: 'none', borderRadius: '6px', cursor: 'pointer', fontSize: '13px' }}
        >
          Refresh
        </button>
        <button
          onClick={handleExport}
          style={{ padding: '7px 14px', background: '#f3f4f6', color: '#374151', border: '1px solid #d1d5db', borderRadius: '6px', cursor: 'pointer', fontSize: '13px' }}
        >
          Export JSONL
        </button>
      </div>

      {error && (
        <div style={{ padding: '10px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>
          {error}
        </div>
      )}

      <div style={{ background: '#fff', borderRadius: '8px', overflow: 'hidden', border: '1px solid #e5e7eb' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr>
              <th style={headerCell}>When</th>
              <th style={headerCell}>Actor</th>
              <th style={headerCell}>Action</th>
              <th style={headerCell}>Resource</th>
              <th style={headerCell}>Status</th>
              <th style={headerCell}>IP</th>
              <th style={headerCell}>Details</th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr><td colSpan={7} style={{ ...cell, textAlign: 'center', color: '#6b7280' }}>Loading…</td></tr>
            )}
            {!loading && entries.length === 0 && (
              <tr><td colSpan={7} style={{ ...cell, textAlign: 'center', color: '#6b7280', padding: '24px' }}>No audit entries.</td></tr>
            )}
            {entries.map((e) => (
              <tr key={e.id}>
                <td style={cell}>{new Date(e.occurred_at).toLocaleString()}</td>
                <td style={cell}>{e.actor_label || e.user_id || e.actor_kind}</td>
                <td style={{ ...cell, fontFamily: 'monospace' }}>{e.action}</td>
                <td style={cell}>
                  {e.resource_type}
                  {e.resource_id && <span style={{ color: '#6b7280' }}> · <code style={{ fontSize: '11px' }}>{e.resource_id.slice(0, 8)}…</code></span>}
                </td>
                <td style={cell}>
                  <span style={{ padding: '2px 6px', borderRadius: '4px', color: '#fff', background: statusBadgeColor(e.status_code), fontSize: '11px', fontWeight: 600 }}>
                    {e.status_code}
                  </span>
                </td>
                <td style={cell}>{e.ip_address || '—'}</td>
                <td style={{ ...cell, maxWidth: '320px', wordBreak: 'break-word' }}>
                  {e.details && Object.keys(e.details).length > 0 ? (
                    <code style={{ fontSize: '11px', color: '#6b7280' }}>{JSON.stringify(e.details)}</code>
                  ) : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: '12px', fontSize: '12px', color: '#6b7280' }}>
        <span>{total.toLocaleString()} entries</span>
        <div style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
          <button disabled={page <= 1} onClick={() => setPage((p) => p - 1)} style={{ padding: '6px 10px', borderRadius: '4px', border: '1px solid #d1d5db', background: '#fff', cursor: page <= 1 ? 'not-allowed' : 'pointer' }}>Prev</button>
          <span>Page {page} / {pages}</span>
          <button disabled={page >= pages} onClick={() => setPage((p) => p + 1)} style={{ padding: '6px 10px', borderRadius: '4px', border: '1px solid #d1d5db', background: '#fff', cursor: page >= pages ? 'not-allowed' : 'pointer' }}>Next</button>
        </div>
      </div>
    </div>
  )
}
