import { useCallback, useEffect, useMemo, useState } from 'react'
import * as oauthService from '../../services/oauthService'
import type { OAuthProvider, OAuthGrant } from '../../services/oauthService'
import OAuthConnect from './OAuthConnect'

interface OAuthGrantSelectorProps {
  // Currently selected grant id (stored on the node config as oauth_grant_id).
  value?: string
  onChange: (grantId: string | undefined) => void
  connectionId?: string
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '8px 10px',
  marginBottom: '8px',
  border: '1px solid #d1d5db',
  borderRadius: '4px',
  fontSize: '12px',
  boxSizing: 'border-box',
  color: '#374151',
  backgroundColor: '#ffffff',
}

// Known provider types whose auth/token URLs + scopes the backend fills in
// automatically (applyProfileDefaults). "custom" requires explicit URLs.
const PROVIDER_TYPES = ['google', 'microsoft', 'salesforce', 'hubspot', 'shopify', 'custom']

const defaultRedirectURL = () =>
  typeof window !== 'undefined' ? `${window.location.origin}/api/v1/oauth/callback` : ''

/**
 * Lets the user pick an existing OAuth grant for a node, or connect a new one.
 * Flow: choose a configured provider → either pick one of its existing grants
 * or click "Connect" to run the popup auth flow. Surfaces a "Reconnect
 * required" badge when a grant's refresh has permanently failed, and offers a
 * revoke button (completes acceptance criterion #4: revoke from the UI).
 *
 * Also includes an inline "Add provider" form so an owner can register an OAuth
 * app (client id/secret) without leaving the editor.
 */
export default function OAuthGrantSelector({ value, onChange, connectionId }: OAuthGrantSelectorProps) {
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [grants, setGrants] = useState<OAuthGrant[]>([])
  const [providerId, setProviderId] = useState<string>('')
  const [shop, setShop] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Add-provider form state.
  const [addOpen, setAddOpen] = useState(false)
  const [fName, setFName] = useState('')
  const [fType, setFType] = useState('google')
  const [fClientId, setFClientId] = useState('')
  const [fSecret, setFSecret] = useState('')
  const [fRedirect, setFRedirect] = useState(defaultRedirectURL())
  const [fAuthURL, setFAuthURL] = useState('')
  const [fTokenURL, setFTokenURL] = useState('')
  const [saving, setSaving] = useState(false)
  const [addError, setAddError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [provs, grs] = await Promise.all([
        oauthService.listProviders(),
        oauthService.listGrants(),
      ])
      setProviders(provs)
      setGrants(grs)
      // Default the provider dropdown to the selected grant's provider, else first.
      if (!providerId) {
        const selected = grs.find((g) => g.id === value)
        setProviderId(selected?.provider_id || provs[0]?.id || '')
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load OAuth data')
    } finally {
      setLoading(false)
    }
  }, [providerId, value])

  useEffect(() => {
    void refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const selectedProvider = useMemo(
    () => providers.find((p) => p.id === providerId),
    [providers, providerId]
  )

  // Grants belonging to the chosen provider, newest-usable first.
  const providerGrants = useMemo(
    () => grants.filter((g) => g.provider_id === providerId && !g.revoked_at),
    [grants, providerId]
  )

  const isShopify = selectedProvider?.provider_type === 'shopify'

  const handleConnected = useCallback(
    (grantId: string) => {
      void refresh()
      onChange(grantId)
    },
    [refresh, onChange]
  )

  const handleRevoke = async (grantId: string) => {
    if (!window.confirm('Revoke this connection? Any node using it will stop working until reconnected.')) return
    try {
      await oauthService.revokeGrant(grantId)
      if (value === grantId) onChange(undefined)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Revoke failed')
    }
  }

  const handleCreateProvider = async () => {
    if (!fName.trim() || !fClientId.trim() || !fSecret.trim() || !fRedirect.trim()) {
      setAddError('Name, client id, client secret and redirect URL are required')
      return
    }
    if (fType === 'custom' && (!fAuthURL.trim() || !fTokenURL.trim())) {
      setAddError('Custom providers need an auth URL and a token URL')
      return
    }
    setSaving(true)
    setAddError(null)
    try {
      const created = await oauthService.createProvider({
        name: fName.trim(),
        provider_type: fType,
        client_id: fClientId.trim(),
        client_secret: fSecret,
        redirect_url: fRedirect.trim(),
        ...(fType === 'custom' ? { auth_url: fAuthURL.trim(), token_url: fTokenURL.trim() } : {}),
      })
      // Reset the form, select the new provider, reload lists.
      setAddOpen(false)
      setFName(''); setFClientId(''); setFSecret(''); setFAuthURL(''); setFTokenURL('')
      await refresh()
      if (created?.id) setProviderId(created.id)
    } catch (e) {
      setAddError(e instanceof Error ? e.message : 'Failed to create provider')
    } finally {
      setSaving(false)
    }
  }

  // Inline "Add OAuth provider" form (shared by the empty + populated states).
  const addProviderForm = (
    <div style={{ marginTop: '8px' }}>
      {!addOpen ? (
        <button
          type="button"
          onClick={() => { setAddOpen(true); setFRedirect(defaultRedirectURL()) }}
          style={{
            padding: '4px 10px', fontSize: '12px', fontWeight: 600,
            color: '#2563eb', backgroundColor: '#eff6ff',
            border: '1px solid #bfdbfe', borderRadius: '4px', cursor: 'pointer',
          }}
        >
          + Add OAuth provider
        </button>
      ) : (
        <div style={{ border: '1px solid #e5e7eb', borderRadius: '6px', padding: '10px', backgroundColor: '#f9fafb' }}>
          <div style={{ fontSize: '12px', fontWeight: 600, color: '#374151', marginBottom: '8px' }}>New OAuth provider</div>
          <input style={inputStyle} placeholder="Name (e.g. Google – My Project)" value={fName} onChange={(e) => setFName(e.target.value)} />
          <select style={inputStyle} value={fType} onChange={(e) => setFType(e.target.value)}>
            {PROVIDER_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
          <input style={inputStyle} placeholder="Client ID" value={fClientId} onChange={(e) => setFClientId(e.target.value)} />
          <input style={inputStyle} type="password" placeholder="Client secret" value={fSecret} onChange={(e) => setFSecret(e.target.value)} autoComplete="new-password" />
          <input style={inputStyle} placeholder="Redirect URL" value={fRedirect} onChange={(e) => setFRedirect(e.target.value)} />
          {fType === 'custom' && (
            <>
              <input style={inputStyle} placeholder="Authorize URL" value={fAuthURL} onChange={(e) => setFAuthURL(e.target.value)} />
              <input style={inputStyle} placeholder="Token URL" value={fTokenURL} onChange={(e) => setFTokenURL(e.target.value)} />
            </>
          )}
          <div style={{ fontSize: '11px', color: '#6b7280', marginBottom: '8px' }}>
            Register this exact Redirect URL with the provider. For known types the auth/token URLs are filled automatically.
          </div>
          {addError && <p style={{ fontSize: '11px', color: '#dc2626', marginBottom: '6px' }}>{addError}</p>}
          <div style={{ display: 'flex', gap: '6px' }}>
            <button
              type="button" disabled={saving} onClick={handleCreateProvider}
              style={{ padding: '4px 10px', fontSize: '12px', fontWeight: 600, color: '#fff', backgroundColor: '#2563eb', border: 'none', borderRadius: '4px', cursor: 'pointer' }}
            >
              {saving ? 'Saving…' : 'Save provider'}
            </button>
            <button
              type="button" disabled={saving} onClick={() => { setAddOpen(false); setAddError(null) }}
              style={{ padding: '4px 10px', fontSize: '12px', color: '#374151', backgroundColor: '#fff', border: '1px solid #d1d5db', borderRadius: '4px', cursor: 'pointer' }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )

  if (loading && providers.length === 0) {
    return <p style={{ fontSize: '12px', color: '#6b7280' }}>Loading OAuth providers…</p>
  }

  if (providers.length === 0) {
    return (
      <div>
        <p style={{ fontSize: '12px', color: '#92400e', marginBottom: '4px' }}>
          No OAuth providers configured yet. Add one to connect an account.
        </p>
        {addProviderForm}
        {error && <p style={{ marginTop: '6px', fontSize: '12px', color: '#dc2626' }}>{error}</p>}
      </div>
    )
  }

  return (
    <div>
      {/* Provider picker */}
      <select
        value={providerId}
        onChange={(e) => {
          setProviderId(e.target.value)
          onChange(undefined) // clear selected grant when switching provider
        }}
        style={inputStyle}
      >
        {providers.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name} ({p.provider_type})
          </option>
        ))}
      </select>

      {/* Existing grants for this provider */}
      {providerGrants.length > 0 && (
        <select value={value || ''} onChange={(e) => onChange(e.target.value || undefined)} style={inputStyle}>
          <option value="">— Select a connection —</option>
          {providerGrants.map((g) => (
            <option key={g.id} value={g.id}>
              {g.user_identifier || g.id}
              {g.needs_reconnect ? ' (reconnect required)' : ''}
            </option>
          ))}
        </select>
      )}

      {/* Reconnect / revoke controls for the selected grant */}
      {value && providerGrants.some((g) => g.id === value) && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '8px' }}>
          {providerGrants.find((g) => g.id === value)?.needs_reconnect && (
            <span style={{ fontSize: '11px', color: '#b45309', fontWeight: 600 }}>
              ⚠ Reconnect required
            </span>
          )}
          <button
            type="button"
            onClick={() => handleRevoke(value)}
            style={{
              padding: '2px 8px',
              fontSize: '11px',
              color: '#dc2626',
              backgroundColor: '#fef2f2',
              border: '1px solid #fecaca',
              borderRadius: '4px',
              cursor: 'pointer',
            }}
          >
            Revoke
          </button>
        </div>
      )}

      {/* Shopify needs the shop subdomain before connecting */}
      {isShopify && (
        <input
          type="text"
          placeholder="your-store (from your-store.myshopify.com)"
          value={shop}
          onChange={(e) => setShop(e.target.value.trim())}
          style={inputStyle}
        />
      )}

      <OAuthConnect
        providerId={providerId}
        providerLabel={selectedProvider?.name || 'provider'}
        connectionId={connectionId}
        extraParams={isShopify && shop ? { shop } : undefined}
        disabled={isShopify && !shop}
        onConnected={handleConnected}
      />

      {addProviderForm}

      {error && <p style={{ marginTop: '6px', fontSize: '12px', color: '#dc2626' }}>{error}</p>}
    </div>
  )
}
