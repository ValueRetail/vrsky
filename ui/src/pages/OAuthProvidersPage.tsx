import { useState, useEffect, useCallback } from 'react'
import { useUIStore } from '@/store/uiStore'
import { useAuthStore } from '@/store/authStore'
import * as oauthService from '@/services/oauthService'
import type { OAuthProvider } from '@/services/oauthService'

// Values must match the backend provider registry keys (pkg/oauth DefaultRegistry)
// so known types get their auth/token URLs + scopes filled by applyProfileDefaults.
const PROVIDER_TYPES = ['google', 'microsoft365', 'salesforce', 'hubspot', 'shopify', 'custom']

const defaultRedirectURL = () =>
  typeof window !== 'undefined' ? `${window.location.origin}/api/v1/oauth/callback` : ''

const inputClass =
  'w-full px-3 py-2 border border-neutral-300 rounded-md text-sm text-neutral-900 bg-white focus:outline-none focus:ring-2 focus:ring-blue-500'
const labelClass = 'block text-xs font-medium text-neutral-600 mb-1'

/**
 * Settings → OAuth providers. Owner-managed registration of OAuth apps
 * (client id/secret + URLs). Provider CRUD endpoints are admin-gated server-side.
 * Pipeline nodes only *select* a provider/grant (see OAuthGrantSelector).
 */
export default function OAuthProvidersPage() {
  const { addNotification, showConfirmDialog, hideConfirmDialog } = useUIStore()

  // Provider CRUD endpoints are admin-gated server-side; mirror that in the UI
  // so non-admins see a read-only view instead of controls that 403.
  const role = useAuthStore((s) => s.currentTenant?.user_role)
  const canManage = role === 'owner' || role === 'admin'

  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [loading, setLoading] = useState(true)

  const [name, setName] = useState('')
  const [type, setType] = useState('google')
  const [clientId, setClientId] = useState('')
  const [secret, setSecret] = useState('')
  const [redirect, setRedirect] = useState(defaultRedirectURL())
  const [authURL, setAuthURL] = useState('')
  const [tokenURL, setTokenURL] = useState('')
  const [saving, setSaving] = useState(false)

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      setProviders(await oauthService.listProviders())
    } catch (e) {
      addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to load providers' })
    } finally {
      setLoading(false)
    }
  }, [addNotification])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const resetForm = () => {
    setName(''); setClientId(''); setSecret(''); setAuthURL(''); setTokenURL('')
    setRedirect(defaultRedirectURL())
  }

  const handleCreate = async () => {
    if (!name.trim() || !clientId.trim() || !secret.trim() || !redirect.trim()) {
      addNotification({ type: 'error', title: 'Missing fields', message: 'Name, client id, client secret and redirect URL are required.' })
      return
    }
    if (type === 'custom' && (!authURL.trim() || !tokenURL.trim())) {
      addNotification({ type: 'error', title: 'Missing URLs', message: 'Custom providers need an authorize URL and a token URL.' })
      return
    }
    setSaving(true)
    try {
      await oauthService.createProvider({
        name: name.trim(),
        provider_type: type,
        client_id: clientId.trim(),
        client_secret: secret.trim(),
        redirect_url: redirect.trim(),
        ...(type === 'custom' ? { auth_url: authURL.trim(), token_url: tokenURL.trim() } : {}),
      })
      addNotification({ type: 'success', title: 'Provider added', message: `${name.trim()} is ready to connect.` })
      resetForm()
      await refresh()
    } catch (e) {
      addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to create provider' })
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = (p: OAuthProvider) => {
    showConfirmDialog({
      title: 'Delete provider',
      message: `Delete "${p.name}"? Existing grants for this provider will stop working.`,
      confirmLabel: 'Delete',
      destructive: true,
      onConfirm: async () => {
        hideConfirmDialog()
        try {
          await oauthService.deleteProvider(p.id)
          addNotification({ type: 'success', title: 'Deleted', message: `${p.name} removed.` })
          await refresh()
        } catch (e) {
          addNotification({ type: 'error', title: 'Error', message: e instanceof Error ? e.message : 'Failed to delete provider' })
        }
      },
    })
  }

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <h1 className="text-2xl font-bold text-neutral-900 mb-2">OAuth Providers</h1>
      <p className="text-sm text-neutral-600 mb-6">
        Register OAuth apps (client id/secret) once here; pipeline nodes then connect accounts and
        pick a provider. Register the redirect URL below with the provider exactly as shown.
      </p>

      {!canManage && (
        <div className="bg-amber-50 border border-amber-200 text-amber-800 rounded-lg p-4 mb-6 text-sm">
          Only workspace <strong>owners</strong> and <strong>admins</strong> can register or delete OAuth providers.
          You can view the configured providers below.
        </div>
      )}

      {/* Add provider — owners/admins only */}
      {canManage && (
      <div className="bg-white border border-neutral-200 rounded-lg p-5 mb-6">
        <h2 className="text-sm font-semibold text-neutral-800 mb-3">Add a provider</h2>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className={labelClass}>Name</label>
            <input className={inputClass} placeholder="Google – My Project" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <label className={labelClass}>Type</label>
            <select className={inputClass} value={type} onChange={(e) => setType(e.target.value)}>
              {PROVIDER_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          </div>
          <div>
            <label className={labelClass}>Client ID</label>
            <input className={inputClass} value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div>
            <label className={labelClass}>Client secret</label>
            <input className={inputClass} type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} />
          </div>
          <div className="col-span-2">
            <label className={labelClass}>Redirect URL</label>
            <input className={inputClass} value={redirect} onChange={(e) => setRedirect(e.target.value)} />
          </div>
          {type === 'custom' && (
            <>
              <div>
                <label className={labelClass}>Authorize URL</label>
                <input className={inputClass} value={authURL} onChange={(e) => setAuthURL(e.target.value)} />
              </div>
              <div>
                <label className={labelClass}>Token URL</label>
                <input className={inputClass} value={tokenURL} onChange={(e) => setTokenURL(e.target.value)} />
              </div>
            </>
          )}
        </div>
        <p className="text-xs text-neutral-500 mt-2">
          For known types the authorize/token URLs are filled automatically. Register the redirect URL
          with the provider character-for-character.
        </p>
        <button
          disabled={saving}
          onClick={handleCreate}
          className="mt-3 px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-sm font-semibold rounded-md"
        >
          {saving ? 'Saving…' : 'Add provider'}
        </button>
      </div>
      )}

      {/* List */}
      <h2 className="text-sm font-semibold text-neutral-800 mb-2">Configured providers</h2>
      {loading ? (
        <p className="text-neutral-500">Loading…</p>
      ) : providers.length === 0 ? (
        <p className="text-sm text-neutral-500">No providers configured yet.</p>
      ) : (
        <div className="space-y-2">
          {providers.map((p) => (
            <div key={p.id} className="bg-white border border-neutral-200 rounded-lg p-4 flex items-center justify-between">
              <div className="min-w-0">
                <p className="font-medium text-neutral-900 truncate">{p.name} <span className="text-xs text-neutral-500">({p.provider_type})</span></p>
                <p className="text-xs text-neutral-500 truncate">client: {p.client_id}</p>
                <p className="text-xs text-neutral-500 truncate">redirect: {p.redirect_url}</p>
              </div>
              {canManage && (
                <button
                  onClick={() => handleDelete(p)}
                  className="ml-4 shrink-0 px-3 py-1.5 text-xs font-medium text-red-600 bg-red-50 border border-red-200 rounded-md hover:bg-red-100"
                >
                  Delete
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
