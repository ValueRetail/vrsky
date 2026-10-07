/**
 * Platform → Workspaces (plans/paid-plans.md). Operators only: every workspace
 * with its plan and billing state, open plan requests first, and the one write
 * — set the plan (which copies the tier's limits, closes the request and
 * restarts pipelines a suspension stopped). The server refuses anyone not in
 * PLATFORM_OPERATORS; this page is simply not linked for anyone else.
 */

import { useEffect, useState } from 'react'
import { listTenants, setPlan } from '@/services/platformService'
import type { PlanName, PlatformTenant } from '@/types/models'
import { planTitles } from '@/utils/plan'

const cell: React.CSSProperties = { padding: '10px 12px', borderBottom: '1px solid #f3f4f6', fontSize: '13px', verticalAlign: 'top' }
const headerCell: React.CSSProperties = { ...cell, fontWeight: 600, background: '#f9fafb', whiteSpace: 'nowrap' }
const input: React.CSSProperties = { fontSize: '12px', padding: '4px 6px', border: '1px solid #d1d5db', borderRadius: '4px' }
const button: React.CSSProperties = { padding: '4px 10px', fontSize: '12px', borderRadius: '4px', border: '1px solid #d1d5db', background: '#fff', cursor: 'pointer' }

function statusLabel(t: PlatformTenant): string {
  if (t.billing_status === 'suspended') return 'Suspended'
  if (t.billing_status === 'trial') return t.trial_ends_at ? `Trial until ${new Date(t.trial_ends_at).toLocaleDateString()}` : 'Trial'
  return 'Paid'
}

function toDateInput(iso?: string): string {
  return iso ? iso.slice(0, 10) : ''
}

function Row({ t, onSaved, onError }: { t: PlatformTenant; onSaved: (msg: string) => void; onError: (msg: string) => void }) {
  const [plan, setPlanName] = useState<PlanName>(t.plan)
  const [trialEnds, setTrialEnds] = useState(toDateInput(t.trial_ends_at))
  const [note, setNote] = useState(t.billing_note)
  const [busy, setBusy] = useState(false)

  const save = async () => {
    setBusy(true)
    try {
      const res = await setPlan(t.id, {
        plan,
        trial_ends_at: plan === 'trial' && trialEnds ? new Date(trialEnds + 'T23:59:59Z').toISOString() : undefined,
        note: note !== t.billing_note ? note : undefined,
      })
      onSaved(`${t.name}: ${planTitles[res.plan] ?? res.plan} (${res.billing_status})` +
        (res.pipelines_resumed ? `, ${res.pipelines_resumed} pipeline(s) started again` : ''))
    } catch (e) {
      onError(e instanceof Error ? e.message : 'Failed to set the plan')
    } finally {
      setBusy(false)
    }
  }

  const changed = plan !== t.plan || note !== t.billing_note || (plan === 'trial' && trialEnds !== toDateInput(t.trial_ends_at))
  return (
    <tr style={{ background: t.open_request ? '#eff6ff' : undefined }}>
      <td style={cell}>
        <div style={{ fontWeight: 600 }}>{t.name}</div>
        <div style={{ fontSize: '11px', color: '#6b7280' }}>{t.owner_email || '—'} · since {new Date(t.created_at).toLocaleDateString()}</div>
      </td>
      <td style={cell}>
        <div>{statusLabel(t)}</div>
        <div style={{ fontSize: '11px', color: '#6b7280' }}>{planTitles[t.plan] ?? t.plan}</div>
      </td>
      <td style={{ ...cell, textAlign: 'right' }}>{t.messages_30d.toLocaleString()}</td>
      <td style={{ ...cell, textAlign: 'right' }}>{t.running_pipelines}</td>
      <td style={cell}>
        {t.open_request ? (
          <div>
            <strong>Wants {planTitles[t.open_request.requested_plan] ?? t.open_request.requested_plan}</strong>
            <div style={{ fontSize: '11px', color: '#6b7280' }}>{new Date(t.open_request.created_at).toLocaleString()}</div>
            {t.open_request.message && <div style={{ fontSize: '12px', whiteSpace: 'pre-wrap' }}>{t.open_request.message}</div>}
          </div>
        ) : <span style={{ color: '#9ca3af' }}>—</span>}
      </td>
      <td style={cell}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
          <select aria-label={`Plan for ${t.name}`} value={plan} onChange={(e) => setPlanName(e.target.value as PlanName)} style={input}>
            <option value="trial">Trial</option>
            <option value="paid">Paid</option>
            <option value="enterprise">Enterprise</option>
          </select>
          {plan === 'trial' && (
            <input aria-label={`Trial end for ${t.name}`} type="date" value={trialEnds} onChange={(e) => setTrialEnds(e.target.value)} style={input} />
          )}
          <input aria-label={`Note for ${t.name}`} placeholder="Note (invoice no., contact)" value={note} onChange={(e) => setNote(e.target.value)} style={input} />
          <button onClick={save} disabled={busy || !changed} style={{ ...button, background: changed ? '#2563eb' : '#fff', color: changed ? '#fff' : '#9ca3af', borderColor: changed ? '#2563eb' : '#d1d5db' }}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
      </td>
    </tr>
  )
}

export default function PlatformTenantsPage() {
  const [rows, setRows] = useState<PlatformTenant[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [version, setVersion] = useState(0)

  useEffect(() => {
    let ignore = false
    listTenants()
      .then((r) => { if (!ignore) { setRows(r); setError(null) } })
      .catch((e) => { if (!ignore) setError(e instanceof Error ? e.message : 'Failed to load workspaces') })
    return () => { ignore = true }
  }, [version])

  const open = (rows ?? []).filter((t) => t.open_request).length

  return (
    <div style={{ padding: '20px', maxWidth: '1100px', margin: '0 auto' }}>
      <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '6px' }}>Workspaces</h1>
      <p style={{ fontSize: '13px', color: '#6b7280', marginBottom: '20px' }}>
        Every workspace on this VRSky. Setting a plan copies the tier&apos;s limits, closes the workspace&apos;s request and
        starts the pipelines a suspension stopped. {open > 0 && <strong>{open} open request{open === 1 ? '' : 's'}.</strong>}
      </p>
      {error && <div role="alert" style={{ padding: '10px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>{error}</div>}
      {notice && <div role="status" style={{ padding: '10px', background: '#f0fdf4', color: '#166534', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>{notice}</div>}
      {rows === null ? (
        <div>Loading…</div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse', background: '#fff', border: '1px solid #e5e7eb', borderRadius: '8px' }}>
          <thead>
            <tr>
              <th style={headerCell}>Workspace</th>
              <th style={headerCell}>Status</th>
              <th style={{ ...headerCell, textAlign: 'right' }}>Messages 30d</th>
              <th style={{ ...headerCell, textAlign: 'right' }}>Running</th>
              <th style={headerCell}>Request</th>
              <th style={headerCell}>Set plan</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((t) => (
              <Row key={t.id + ':' + version} t={t} onSaved={(m) => { setNotice(m); setVersion((v) => v + 1) }} onError={setError} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
