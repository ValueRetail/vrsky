import { useEffect } from 'react'

/**
 * Landing page the OAuth callback redirects the popup to
 * (/oauth/connected?grant_id=...). It posts the new grant id back to the
 * window that opened it, then closes itself. The opener (OAuthConnect)
 * listens for that message to refresh its grant list.
 *
 * Public route — by this point the grant already exists server-side; this
 * page carries no secrets, only the grant id.
 */
export default function OAuthConnected() {
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const grantId = params.get('grant_id') || ''
    const error = params.get('error') || ''

    if (window.opener) {
      // Target the app's own origin only — never '*' — so the message can't
      // be read by another window/site.
      window.opener.postMessage(
        { type: 'vrsky-oauth-connected', grantId, error },
        window.location.origin
      )
      window.close()
    }
  }, [])

  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100vh', fontFamily: 'system-ui, sans-serif', color: '#374151' }}>
      <div style={{ textAlign: 'center' }}>
        <p style={{ fontSize: '15px', fontWeight: 600 }}>Connection complete</p>
        <p style={{ fontSize: '13px', color: '#6b7280' }}>You can close this window and return to VRSky.</p>
      </div>
    </div>
  )
}
