/**
 * Settings → Plan (plans/paid-plans.md).
 *
 * What the workspace is on, when the trial ends, the tiers, and the one action
 * an owner has: ask for a plan. Payment happens outside VRSky — the operator
 * is told, invoices, and activates; the page shows the request as open until
 * then. The server enforces owner-only; the UI only hides what it would refuse.
 */

import { useEffect, useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import { getBilling, requestPlan } from '@/services/billingService'
import type { PlanLimits, PlanName, TenantBilling } from '@/types/models'
import { daysLeft, fmtLimit, fmtStorage, planTitles } from '@/utils/plan'

const card: React.CSSProperties = {
  background: '#fff', border: '1px solid #e5e7eb', borderRadius: '8px', padding: '16px', marginBottom: '12px',
}
const label: React.CSSProperties = { fontSize: '11px', color: '#6b7280', display: 'block', marginBottom: '4px' }

const planBlurb: Record<PlanName, string> = {
  trial: '14 days to try VRSky with your own systems.',
  paid: 'For a shop or a chain running its integrations on VRSky. Pricing on request.',
  enterprise: 'Negotiated limits, SLA, SSO. Talk to us.',
}

function PlanCard({ p, current, onAsk, asking, canAsk }: {
  p: PlanLimits; current: boolean; onAsk?: () => void; asking: boolean; canAsk: boolean
}) {
  return (
    <div style={{ ...card, flex: 1, minWidth: '220px', borderColor: current ? '#2563eb' : '#e5e7eb' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
        <strong style={{ fontSize: '15px' }}>{planTitles[p.plan_name] ?? p.plan_name}</strong>
        {current && <span style={{ fontSize: '11px', color: '#2563eb', fontWeight: 600 }}>Current</span>}
      </div>
      <p style={{ fontSize: '12px', color: '#6b7280', margin: '4px 0 12px' }}>{planBlurb[p.plan_name] ?? ''}</p>
      <ul style={{ fontSize: '12px', margin: 0, paddingLeft: '16px', color: '#374151' }}>
        <li>{fmtLimit(p.max_integrations)} integrations</li>
        <li>{fmtLimit(p.max_msg_per_sec, ' msg/s')}</li>
        <li>{fmtStorage(p.max_storage_bytes)} storage</li>
        {p.included_messages_per_month > 0 && <li>{p.included_messages_per_month.toLocaleString()} messages/month</li>}
      </ul>
      {onAsk && canAsk && (
        <button
          onClick={onAsk}
          disabled={asking}
          style={{ marginTop: '14px', padding: '8px 14px', background: '#2563eb', color: '#fff', border: 'none', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}
        >
          {p.plan_name === 'enterprise' ? 'Talk to us about Enterprise' : 'Request the Paid plan'}
        </button>
      )}
    </div>
  )
}

export default function PlanPage() {
  const { currentTenant } = useAuthStore()
  const role = currentTenant?.user_role
  const isOwner = role === 'owner'
  const [b, setB] = useState<TenantBilling | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [asking, setAsking] = useState<PlanName | null>(null)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!currentTenant) return
    let ignore = false
    setLoading(true)
    setError(null)
    getBilling(currentTenant.id)
      .then((next) => { if (!ignore) setB(next) })
      .catch((e) => { if (!ignore) setError(e instanceof Error ? e.message : 'Failed to load plan') })
      .finally(() => { if (!ignore) setLoading(false) })
    return () => { ignore = true }
  }, [currentTenant?.id])

  const send = async () => {
    if (!currentTenant || !asking) return
    setBusy(true)
    setError(null)
    try {
      const req = await requestPlan(currentTenant.id, asking, message.trim())
      setB((prev) => (prev ? { ...prev, open_request: req } : prev))
      setAsking(null)
      setMessage('')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to send the request')
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <div style={{ padding: '20px' }}>Loading…</div>
  if (!b) return <div style={{ padding: '20px' }}>{error ?? 'No plan data.'}</div>

  const days = daysLeft(b.trial_ends_at)
  const status = b.billing_status

  return (
    <div style={{ padding: '20px', maxWidth: '900px', margin: '0 auto' }}>
      <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '6px' }}>Plan</h1>
      <p style={{ fontSize: '13px', color: '#6b7280', marginBottom: '20px' }}>
        Plans are activated by VRSky after you ask; there is no card to enter here.
      </p>

      {error && (
        <div role="alert" style={{ padding: '10px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>
          {error}
        </div>
      )}

      <div style={card}>
        <span style={label}>Your workspace</span>
        {status === 'suspended' && (
          <p style={{ margin: 0, fontSize: '14px' }}>
            <strong style={{ color: '#991b1b' }}>Trial ended — pipelines are stopped.</strong> Your configuration, secrets and
            history are kept. Request a plan below and they start again once it is active.
          </p>
        )}
        {status === 'trial' && (
          <p style={{ margin: 0, fontSize: '14px' }}>
            <strong>On trial</strong>
            {days !== null && <> · {days === 0 ? 'ends today' : `${days} day${days === 1 ? '' : 's'} left`}</>}
            {b.trial_ends_at && <span style={{ color: '#6b7280' }}> (until {new Date(b.trial_ends_at).toLocaleDateString()})</span>}
            . When it ends, pipelines stop until a plan is active.
          </p>
        )}
        {status === 'paid' && (
          <p style={{ margin: 0, fontSize: '14px' }}>
            <strong>{planTitles[b.plan] ?? b.plan} plan</strong> · active.
          </p>
        )}
      </div>

      {b.open_request && (
        <div role="status" style={{ ...card, background: '#eff6ff', borderColor: '#bfdbfe' }}>
          <strong style={{ fontSize: '14px' }}>Requested: {planTitles[b.open_request.requested_plan] ?? b.open_request.requested_plan}</strong>
          <p style={{ fontSize: '12px', color: '#374151', margin: '4px 0 0' }}>
            Sent {new Date(b.open_request.created_at).toLocaleString()} — we&apos;ll be in touch to arrange invoicing, then activate it.
          </p>
        </div>
      )}

      <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
        {b.plans.map((p) => (
          <PlanCard
            key={p.plan_name}
            p={p}
            current={p.plan_name === b.plan}
            asking={busy}
            canAsk={isOwner && !b.open_request && p.plan_name !== 'trial' && p.plan_name !== b.plan}
            onAsk={p.plan_name !== 'trial' ? () => setAsking(p.plan_name) : undefined}
          />
        ))}
      </div>

      {!isOwner && !b.open_request && status !== 'paid' && (
        <p style={{ fontSize: '12px', color: '#6b7280' }}>Only the workspace owner can request a plan.</p>
      )}

      {asking && (
        <div style={card}>
          <strong style={{ fontSize: '14px' }}>Request the {planTitles[asking]} plan</strong>
          <p style={{ fontSize: '12px', color: '#6b7280', margin: '4px 0 10px' }}>
            Anything we should know — how many shops, where to send the invoice, questions.
          </p>
          <textarea
            aria-label="Message to VRSky"
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            rows={3}
            style={{ width: '100%', fontSize: '13px', padding: '8px', border: '1px solid #d1d5db', borderRadius: '6px' }}
          />
          <div style={{ marginTop: '10px', display: 'flex', gap: '8px' }}>
            <button
              onClick={send}
              disabled={busy}
              style={{ padding: '8px 14px', background: '#2563eb', color: '#fff', border: 'none', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}
            >
              {busy ? 'Sending…' : 'Send request'}
            </button>
            <button
              onClick={() => setAsking(null)}
              disabled={busy}
              style={{ padding: '8px 14px', background: '#fff', color: '#374151', border: '1px solid #d1d5db', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
