/**
 * DLQPanel — Failed Messages tab for a deployed pipeline.
 *
 * Lists JetStream DLQ entries with last error + worker + payload preview.
 * Buttons re-publish the message back to the main stream (Retry) or remove
 * it (Discard).
 *
 * Phase 1E (#70).
 */

import { useEffect, useState } from 'react'
import { listDLQ, retryDLQ, discardDLQ, type DLQEntry } from '../../services/dlqService'

interface DLQPanelProps {
  connectionID: string
  /** Optional callback fired when the panel performs a mutating action so
   *  the parent can refresh metrics / notify the user. */
  onChange?: () => void
}

const tableCell: React.CSSProperties = {
  padding: '6px 8px',
  borderBottom: '1px solid #f3f4f6',
  fontSize: '12px',
  verticalAlign: 'top',
}

const errStyle: React.CSSProperties = {
  ...tableCell,
  color: '#b91c1c',
  fontFamily: 'monospace',
  maxWidth: '320px',
  whiteSpace: 'normal',
  wordBreak: 'break-word',
}

const button = (color: string): React.CSSProperties => ({
  padding: '4px 8px',
  background: color,
  color: '#fff',
  border: 'none',
  borderRadius: '4px',
  fontSize: '11px',
  cursor: 'pointer',
})

export default function DLQPanel({ connectionID, onChange }: DLQPanelProps) {
  const [entries, setEntries] = useState<DLQEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<number | null>(null) // sequence currently being acted on

  const refresh = async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await listDLQ(connectionID)
      setEntries(data)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load DLQ')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, 10_000)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectionID])

  const handleRetry = async (seq: number) => {
    if (!window.confirm(`Re-publish message #${seq} back to the pipeline?`)) return
    setBusy(seq)
    try {
      await retryDLQ(connectionID, seq)
      await refresh()
      onChange?.()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Retry failed')
    } finally {
      setBusy(null)
    }
  }

  const handleDiscard = async (seq: number) => {
    if (!window.confirm(`Discard message #${seq}? This cannot be undone.`)) return
    setBusy(seq)
    try {
      await discardDLQ(connectionID, seq)
      await refresh()
      onChange?.()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Discard failed')
    } finally {
      setBusy(null)
    }
  }

  return (
    <div style={{ padding: '12px' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
        <strong style={{ fontSize: '13px', color: '#374151' }}>
          Failed messages ({entries.length})
        </strong>
        <button onClick={refresh} disabled={loading} style={button('#2563eb')}>
          {loading ? 'Loading…' : 'Refresh'}
        </button>
      </div>

      {error && (
        <div style={{ padding: '8px', background: '#fef2f2', color: '#991b1b', fontSize: '12px', marginBottom: '8px', borderRadius: '4px' }}>
          {error}
        </div>
      )}

      {!loading && entries.length === 0 && (
        <div style={{ padding: '24px', textAlign: 'center', color: '#6b7280', fontSize: '13px' }}>
          No failed messages. After {' '}
          <strong>5 consecutive delivery failures</strong>{' '}
          a message lands here.
        </div>
      )}

      {entries.length > 0 && (
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '12px' }}>
          <thead>
            <tr style={{ background: '#f9fafb', textAlign: 'left' }}>
              <th style={{ ...tableCell, fontWeight: 600 }}>#</th>
              <th style={{ ...tableCell, fontWeight: 600 }}>When</th>
              <th style={{ ...tableCell, fontWeight: 600 }}>Worker</th>
              <th style={{ ...tableCell, fontWeight: 600 }}>Last error</th>
              <th style={{ ...tableCell, fontWeight: 600 }}>Size</th>
              <th style={{ ...tableCell, fontWeight: 600 }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.sequence}>
                <td style={tableCell}>{e.sequence}</td>
                <td style={tableCell}>{new Date(e.received_at).toLocaleString()}</td>
                <td style={tableCell}>{e.worker || '—'}</td>
                <td style={errStyle}>{e.last_error || '—'}</td>
                <td style={tableCell}>{e.payload_size} B</td>
                <td style={tableCell}>
                  <div style={{ display: 'flex', gap: '4px' }}>
                    <button
                      disabled={busy === e.sequence}
                      onClick={() => handleRetry(e.sequence)}
                      style={button('#059669')}
                    >
                      Retry
                    </button>
                    <button
                      disabled={busy === e.sequence}
                      onClick={() => handleDiscard(e.sequence)}
                      style={button('#dc2626')}
                    >
                      Discard
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
