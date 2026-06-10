import { useState, useEffect, useCallback } from 'react'
import { useUIStore } from '@/store/uiStore'
import { useAuthStore } from '@/store/authStore'
import * as notificationService from '@/services/notificationService'
import type { NotificationTarget, NotificationTargetType } from '@/services/notificationService'

const TARGET_TYPES: { value: NotificationTargetType; label: string }[] = [
  { value: 'slack', label: 'Slack (incoming webhook)' },
  { value: 'email', label: 'Email' },
  { value: 'pagerduty', label: 'PagerDuty (Events API v2)' },
  { value: 'webhook', label: 'Webhook (POST JSON)' },
]

const SEVERITIES = ['', 'info', 'warning', 'critical']

const inputClass =
  'w-full px-3 py-2 border border-neutral-300 rounded-md text-sm text-neutral-900 bg-white focus:outline-none focus:ring-2 focus:ring-blue-500'
const labelClass = 'block text-xs font-medium text-neutral-600 mb-1'

const typeBadgeColor: Record<string, string> = {
  slack: 'bg-purple-50 text-purple-700 border-purple-200',
  email: 'bg-blue-50 text-blue-700 border-blue-200',
  pagerduty: 'bg-green-50 text-green-700 border-green-200',
  webhook: 'bg-amber-50 text-amber-700 border-amber-200',
}

/**
 * Settings → Notifications (#84). Per-tenant alert delivery targets. Alerts
 * fired by Prometheus route via Alertmanager into the management-api, which
 * fans out to these targets. Write endpoints are admin-gated server-side.
 */
export default function NotificationsPage() {
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()

  const role = useAuthStore((s) => s.currentTenant?.user_role)
  const canManage = role === 'owner' || role === 'admin'

  const [targets, setTargets] = useState<NotificationTarget[]>([])
  const [loading, setLoading] = useState(true)

  const [name, setName] = useState('')
  const [type, setType] = useState<NotificationTargetType>('slack')
  const [secret, setSecret] = useState('')
  const [email, setEmail] = useState('')
  const [url, setUrl] = useState('')
  const [platform, setPlatform] = useState(false)
  const [minSeverity, setMinSeverity] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState<string | null>(null) // target id being tested

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      setTargets(await notificationService.listTargets())
    } catch (e) {
      addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to load targets' })
    } finally {
      setLoading(false)
    }
  }, [addNotification])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const resetForm = () => {
    setName(''); setSecret(''); setEmail(''); setUrl(''); setPlatform(false); setMinSeverity('')
  }

  const handleCreate = async () => {
    if (!name.trim()) {
      addNotification({ type: 'error', title: 'Missing name', message: 'Give the target a name.' })
      return
    }
    if (type === 'slack' && !secret.trim()) {
      addNotification({ type: 'error', title: 'Missing webhook URL', message: 'Slack targets need the incoming-webhook URL.' })
      return
    }
    if (type === 'email' && !email.trim()) {
      addNotification({ type: 'error', title: 'Missing address', message: 'Email targets need a recipient address.' })
      return
    }
    if (type === 'pagerduty' && !secret.trim()) {
      addNotification({ type: 'error', title: 'Missing routing key', message: 'PagerDuty targets need the Events API routing key.' })
      return
    }
    if (type === 'webhook' && !url.trim()) {
      addNotification({ type: 'error', title: 'Missing URL', message: 'Webhook targets need a destination URL.' })
      return
    }
    setSaving(true)
    try {
      await notificationService.createTarget({
        name: name.trim(),
        type,
        ...(secret.trim() ? { secret: secret.trim() } : {}),
        ...(type === 'email' ? { email: email.trim() } : {}),
        ...(type === 'webhook' ? { url: url.trim() } : {}),
        platform,
        ...(minSeverity ? { min_severity: minSeverity } : {}),
      })
      addNotification({ type: 'success', title: 'Target added', message: `${name.trim()} will receive alerts.` })
      resetForm()
      await refresh()
    } catch (e) {
      addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to create target' })
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async (t: NotificationTarget) => {
    setTesting(t.id)
    try {
      const res = await notificationService.testTarget(t.id)
      if (res.ok) {
        addNotification({ type: 'success', title: 'Test sent', message: `Check ${t.type === 'email' ? t.email : t.name} for the test notification.` })
      } else {
        addNotification({ type: 'error', title: 'Test failed', message: res.error || 'Delivery failed' })
      }
    } catch (e) {
      addNotification({ type: 'error', title: 'Test failed', message: e instanceof Error ? e.message : 'Delivery failed' })
    } finally {
      setTesting(null)
    }
  }

  const handleToggle = async (t: NotificationTarget) => {
    try {
      await notificationService.updateTarget(t.id, {
        name: t.name,
        type: t.type,
        email: t.email,
        url: t.url,
        platform: t.platform,
        min_severity: t.min_severity,
        enabled: !t.enabled,
      })
      await refresh()
    } catch (e) {
      addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to update target' })
    }
  }

  const handleDelete = (t: NotificationTarget) => {
    showConfirmDialog({
      title: 'Delete target',
      message: `Delete "${t.name}"? Alerts will no longer be delivered there.`,
      confirmLabel: 'Delete',
      destructive: true,
      onConfirm: async () => {
        hideConfirmDialog()
        try {
          await notificationService.deleteTarget(t.id)
          addNotification({ type: 'success', title: 'Deleted', message: `${t.name} removed.` })
          await refresh()
        } catch (e) {
          addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to delete target' })
        }
      },
    })
  }

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <h1 className="text-2xl font-bold text-neutral-900 mb-2">Notifications</h1>
      <p className="text-sm text-neutral-600 mb-6">
        Where this workspace's alerts are delivered (pipeline down, DLQ growth, …). Targets marked
        “platform” also receive platform-level alerts (disk, brokers, API health).
      </p>

      {!canManage && (
        <div className="bg-amber-50 border border-amber-200 text-amber-800 rounded-lg p-4 mb-6 text-sm">
          Only workspace <strong>owners</strong> and <strong>admins</strong> can manage notification
          targets. You can view the configured targets below.
        </div>
      )}

      {/* Add target — owners/admins only */}
      {canManage && (
        <div className="bg-white border border-neutral-200 rounded-lg p-5 mb-6">
          <h2 className="text-sm font-semibold text-neutral-800 mb-3">Add a target</h2>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className={labelClass}>Name</label>
              <input className={inputClass} placeholder="Ops Slack #alerts" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div>
              <label className={labelClass}>Type</label>
              <select className={inputClass} value={type} onChange={(e) => setType(e.target.value as NotificationTargetType)}>
                {TARGET_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
              </select>
            </div>
            {type === 'slack' && (
              <div className="col-span-2">
                <label className={labelClass}>Incoming webhook URL</label>
                <input className={inputClass} type="password" autoComplete="new-password" placeholder="https://hooks.slack.com/services/…" value={secret} onChange={(e) => setSecret(e.target.value)} />
              </div>
            )}
            {type === 'email' && (
              <div className="col-span-2">
                <label className={labelClass}>Recipient address</label>
                <input className={inputClass} placeholder="ops@example.com" value={email} onChange={(e) => setEmail(e.target.value)} />
              </div>
            )}
            {type === 'pagerduty' && (
              <div className="col-span-2">
                <label className={labelClass}>Events API v2 routing key</label>
                <input className={inputClass} type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} />
              </div>
            )}
            {type === 'webhook' && (
              <>
                <div className="col-span-2">
                  <label className={labelClass}>Destination URL</label>
                  <input className={inputClass} placeholder="https://example.com/alerts" value={url} onChange={(e) => setUrl(e.target.value)} />
                </div>
                <div className="col-span-2">
                  <label className={labelClass}>HMAC signing secret (optional)</label>
                  <input className={inputClass} type="password" autoComplete="new-password" placeholder="adds X-VRSky-Signature" value={secret} onChange={(e) => setSecret(e.target.value)} />
                </div>
              </>
            )}
            <div>
              <label className={labelClass}>Minimum severity</label>
              <select className={inputClass} value={minSeverity} onChange={(e) => setMinSeverity(e.target.value)}>
                {SEVERITIES.map((s) => <option key={s} value={s}>{s === '' ? 'all' : s}</option>)}
              </select>
            </div>
            <div className="flex items-end pb-2">
              <label className="flex items-center gap-2 text-sm text-neutral-700">
                <input type="checkbox" checked={platform} onChange={(e) => setPlatform(e.target.checked)} />
                Also receive platform alerts
              </label>
            </div>
          </div>
          <button
            disabled={saving}
            onClick={handleCreate}
            className="mt-3 px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-sm font-semibold rounded-md"
          >
            {saving ? 'Saving…' : 'Add target'}
          </button>
        </div>
      )}

      {/* List */}
      <h2 className="text-sm font-semibold text-neutral-800 mb-2">Configured targets</h2>
      {loading ? (
        <p className="text-neutral-500">Loading…</p>
      ) : targets.length === 0 ? (
        <p className="text-sm text-neutral-500">No notification targets yet — alerts have nowhere to go.</p>
      ) : (
        <div className="space-y-2">
          {targets.map((t) => (
            <div key={t.id} className="bg-white border border-neutral-200 rounded-lg p-4 flex items-center justify-between">
              <div className="min-w-0">
                <p className="font-medium text-neutral-900 truncate">
                  {t.name}{' '}
                  <span className={`inline-block px-1.5 py-0.5 text-xs border rounded ${typeBadgeColor[t.type] || ''}`}>{t.type}</span>
                  {t.platform && <span className="ml-1 inline-block px-1.5 py-0.5 text-xs border rounded bg-neutral-100 text-neutral-600 border-neutral-200">platform</span>}
                  {!t.enabled && <span className="ml-1 inline-block px-1.5 py-0.5 text-xs border rounded bg-neutral-100 text-neutral-500 border-neutral-200">disabled</span>}
                </p>
                <p className="text-xs text-neutral-500 truncate">
                  {t.type === 'email' ? t.email : t.type === 'webhook' ? t.url : 'secret configured'}
                  {t.min_severity ? ` · min severity: ${t.min_severity}` : ''}
                </p>
              </div>
              {canManage && (
                <div className="ml-4 shrink-0 flex items-center gap-2">
                  <button
                    onClick={() => handleTest(t)}
                    disabled={testing === t.id}
                    className="px-3 py-1.5 text-xs font-medium text-blue-700 bg-blue-50 border border-blue-200 rounded-md hover:bg-blue-100 disabled:opacity-50"
                  >
                    {testing === t.id ? 'Sending…' : 'Test'}
                  </button>
                  <button
                    onClick={() => handleToggle(t)}
                    className="px-3 py-1.5 text-xs font-medium text-neutral-700 bg-neutral-50 border border-neutral-200 rounded-md hover:bg-neutral-100"
                  >
                    {t.enabled ? 'Disable' : 'Enable'}
                  </button>
                  <button
                    onClick={() => handleDelete(t)}
                    className="px-3 py-1.5 text-xs font-medium text-red-600 bg-red-50 border border-red-200 rounded-md hover:bg-red-100"
                  >
                    Delete
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
