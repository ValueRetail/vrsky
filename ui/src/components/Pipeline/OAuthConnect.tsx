import { useCallback, useEffect, useRef, useState } from 'react'
import * as oauthService from '../../services/oauthService'

interface OAuthConnectProps {
  providerId: string
  providerLabel: string
  connectionId?: string
  // Provider-specific extras gathered before launch (e.g. Shopify's shop).
  extraParams?: Record<string, string>
  // Called with the new grant id once the popup completes.
  onConnected: (grantId: string) => void
  disabled?: boolean
}

/**
 * "Connect to <provider>" button. Opens the provider's authorize URL in a
 * popup and waits for the /oauth/connected landing page to postMessage the
 * new grant id back. Robust to the user closing the popup (polls for closure).
 */
export default function OAuthConnect({
  providerId,
  providerLabel,
  connectionId,
  extraParams,
  onConnected,
  disabled,
}: OAuthConnectProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const popupRef = useRef<Window | null>(null)
  const pollRef = useRef<number | null>(null)

  const cleanup = useCallback(() => {
    if (pollRef.current !== null) {
      window.clearInterval(pollRef.current)
      pollRef.current = null
    }
    popupRef.current = null
    setBusy(false)
  }, [])

  useEffect(() => {
    function onMessage(e: MessageEvent) {
      // Only trust messages from our own origin (the popup landing page).
      if (e.origin !== window.location.origin) return
      if (!e.data || e.data.type !== 'vrsky-oauth-connected') return
      if (e.data.error) {
        setError(`Provider error: ${e.data.error}`)
      } else if (e.data.grantId) {
        onConnected(e.data.grantId)
      }
      cleanup()
    }
    window.addEventListener('message', onMessage)
    return () => {
      window.removeEventListener('message', onMessage)
      if (pollRef.current !== null) window.clearInterval(pollRef.current)
    }
  }, [onConnected, cleanup])

  const handleConnect = async () => {
    setError(null)
    setBusy(true)
    try {
      const authorizeURL = await oauthService.startAuth(providerId, {
        connection_id: connectionId,
        extra_params: extraParams,
      })
      const popup = window.open(authorizeURL, 'vrsky-oauth', 'width=600,height=720')
      if (!popup) {
        setError('Popup blocked — allow popups for this site and try again.')
        setBusy(false)
        return
      }
      popupRef.current = popup
      // If the user closes the popup without finishing, stop the spinner.
      pollRef.current = window.setInterval(() => {
        if (popupRef.current && popupRef.current.closed) {
          cleanup()
        }
      }, 500)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to start authorization')
      setBusy(false)
    }
  }

  return (
    <div>
      <button
        type="button"
        onClick={handleConnect}
        disabled={disabled || busy}
        style={{
          padding: '8px 14px',
          fontSize: '13px',
          fontWeight: 600,
          color: '#ffffff',
          backgroundColor: disabled || busy ? '#93c5fd' : '#2563eb',
          border: 'none',
          borderRadius: '6px',
          cursor: disabled || busy ? 'default' : 'pointer',
        }}
      >
        {busy ? 'Waiting for authorization…' : `Connect to ${providerLabel}`}
      </button>
      {error && (
        <p style={{ marginTop: '6px', fontSize: '12px', color: '#dc2626' }}>{error}</p>
      )}
    </div>
  )
}
