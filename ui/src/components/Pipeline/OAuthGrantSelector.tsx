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

/**
 * Lets the user pick an existing OAuth grant for a node, or connect a new one.
 * Flow: choose a configured provider → either pick one of its existing grants
 * or click "Connect" to run the popup auth flow. Surfaces a "Reconnect
 * required" badge when a grant's refresh has permanently failed, and offers a
 * revoke button (completes acceptance criterion #4: revoke from the UI).
 */
export default function OAuthGrantSelector({ value, onChange, connectionId }: OAuthGrantSelectorProps) {
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [grants, setGrants] = useState<OAuthGrant[]>([])
  const [providerId, setProviderId] = useState<string>('')
  const [shop, setShop] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

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

  if (loading && providers.length === 0) {
    return <p style={{ fontSize: '12px', color: '#6b7280' }}>Loading OAuth providers…</p>
  }

  if (providers.length === 0) {
    return (
      <p style={{ fontSize: '12px', color: '#92400e' }}>
        No OAuth providers configured. An admin can add one under Settings → OAuth.
      </p>
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

      {error && <p style={{ marginTop: '6px', fontSize: '12px', color: '#dc2626' }}>{error}</p>}
    </div>
  )
}
