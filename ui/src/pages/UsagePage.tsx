/**
 * Usage / Quotas page — Phase 1I (#74).
 *
 * Shows current usage against each quota and lets owners adjust the
 * limits. Non-owners see the panel read-only.
 */

import { useEffect, useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import { getQuotas, updateQuotas, type TenantQuotas } from '@/services/quotasService'

function fmtBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

const card: React.CSSProperties = {
  background: '#fff',
  border: '1px solid #e5e7eb',
  borderRadius: '8px',
  padding: '16px',
  marginBottom: '12px',
}

const label: React.CSSProperties = {
  display: 'block',
  fontSize: '11px',
  fontWeight: 600,
  color: '#6b7280',
  textTransform: 'uppercase',
  marginBottom: '4px',
}

const input: React.CSSProperties = {
  padding: '7px 10px',
  border: '1px solid #d1d5db',
  borderRadius: '6px',
  fontSize: '13px',
  width: '180px',
}

function bar(value: number, max: number, danger: boolean): JSX.Element {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0
  const color = danger ? '#dc2626' : pct >= 80 ? '#d97706' : '#059669'
  return (
    <div style={{ height: '8px', background: '#f3f4f6', borderRadius: '4px', overflow: 'hidden', marginTop: '6px' }}>
      <div style={{ width: `${pct}%`, height: '100%', background: color }} />
    </div>
  )
}

export default function UsagePage() {
  const { currentTenant } = useAuthStore()
  const [q, setQ] = useState<TenantQuotas | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // Editable copies of the configurable fields.
  const [planName, setPlanName] = useState('')
  const [maxMsg, setMaxMsg] = useState(0)
  const [maxInteg, setMaxInteg] = useState(0)
  const [maxStorage, setMaxStorage] = useState(0)

  const reset = (next: TenantQuotas) => {
    setQ(next)
    setPlanName(next.plan_name)
    setMaxMsg(next.max_msg_per_sec)
    setMaxInteg(next.max_integrations)
    setMaxStorage(next.max_storage_bytes)
  }

  useEffect(() => {
    if (!currentTenant) return
    setLoading(true)
    setError(null)
    getQuotas(currentTenant.id)
      .then(reset)
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load quotas'))
      .finally(() => setLoading(false))
  }, [currentTenant?.id])

  const save = async () => {
    if (!currentTenant || !q) return
    setBusy(true)
    setError(null)
    try {
      const next = await updateQuotas(currentTenant.id, {
        plan_name: planName,
        max_msg_per_sec: maxMsg,
        max_integrations: maxInteg,
        max_storage_bytes: maxStorage,
      })
      reset(next)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save')
    } finally {
      setBusy(false)
    }
  }

  if (loading) {
    return <div style={{ padding: '20px' }}>Loading…</div>
  }
  if (!q) {
    return <div style={{ padding: '20px' }}>{error ?? 'No quota data.'}</div>
  }

  return (
    <div style={{ padding: '20px', maxWidth: '900px', margin: '0 auto' }}>
      <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '6px' }}>Usage & quotas</h1>
      <p style={{ fontSize: '13px', color: '#6b7280', marginBottom: '20px' }}>
        Plan: <strong>{q.plan_name}</strong>. Owners can adjust limits below; 0 means unlimited.
      </p>

      {error && (
        <div style={{ padding: '10px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>
          {error}
        </div>
      )}

      <div style={card}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
          <strong style={{ fontSize: '14px' }}>Storage</strong>
          <span style={{ fontSize: '12px', color: q.storage_exceeded ? '#dc2626' : '#6b7280' }}>
            {fmtBytes(q.storage_bytes)} of {fmtBytes(q.max_storage_bytes)} {q.storage_exceeded && '· over limit'}
          </span>
        </div>
        {bar(q.storage_bytes, q.max_storage_bytes, q.storage_exceeded)}
      </div>

      <div style={card}>
        <strong style={{ fontSize: '14px' }}>Configurable limits</strong>
        <p style={{ fontSize: '11px', color: '#6b7280', margin: '4px 0 14px 0' }}>
          Owners only. Changes are recorded in the audit log.
        </p>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '12px' }}>
          <div>
            <span style={label}>Plan name</span>
            <input style={input} value={planName} onChange={(e) => setPlanName(e.target.value)} />
          </div>
          <div>
            <span style={label}>Max messages/sec</span>
            <input
              type="number" min={0}
              style={input}
              value={maxMsg}
              onChange={(e) => setMaxMsg(parseInt(e.target.value || '0', 10))}
            />
          </div>
          <div>
            <span style={label}>Max active integrations</span>
            <input
              type="number" min={0}
              style={input}
              value={maxInteg}
              onChange={(e) => setMaxInteg(parseInt(e.target.value || '0', 10))}
            />
          </div>
          <div>
            <span style={label}>Max storage (bytes)</span>
            <input
              type="number" min={0}
              style={input}
              value={maxStorage}
              onChange={(e) => setMaxStorage(parseInt(e.target.value || '0', 10))}
            />
          </div>
        </div>
        <button
          onClick={save}
          disabled={busy}
          style={{ marginTop: '14px', padding: '8px 16px', background: '#2563eb', color: '#fff', border: 'none', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}
        >
          {busy ? 'Saving…' : 'Save quotas'}
        </button>
      </div>
    </div>
  )
}
