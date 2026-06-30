/**
 * Invite accept page (#130). Opened from an invite link (/invite?token=...).
 * The visitor must be signed in (route is protected); on accept they're added
 * to the inviting workspace and sent to the dashboard.
 */

import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { acceptInvite } from '@/services/membersService'

export default function InviteAcceptPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const token = params.get('token') || ''
  const [status, setStatus] = useState<'working' | 'ok' | 'error'>(token ? 'working' : 'error')
  const [message, setMessage] = useState(token ? 'Accepting your invite…' : 'This invite link is missing its token.')
  const ran = useRef(false)

  useEffect(() => {
    if (!token || ran.current) return
    ran.current = true
    acceptInvite(token)
      .then(() => {
        setStatus('ok')
        setMessage('You have joined the workspace. Redirecting…')
        setTimeout(() => navigate('/'), 1500)
      })
      .catch((err: unknown) => {
        const ax = err as { response?: { data?: { error?: { message?: string }; message?: string } } }
        setStatus('error')
        setMessage(
          ax.response?.data?.error?.message ||
            ax.response?.data?.message ||
            (err instanceof Error ? err.message : 'Could not accept this invite.'),
        )
      })
  }, [token, navigate])

  const color = status === 'error' ? '#991b1b' : status === 'ok' ? '#065f46' : '#374151'
  const bg = status === 'error' ? '#fef2f2' : status === 'ok' ? '#ecfdf5' : '#f9fafb'

  return (
    <div style={{ maxWidth: '440px', margin: '80px auto', padding: '24px', textAlign: 'center' }}>
      <h1 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '16px' }}>Workspace invite</h1>
      <div style={{ padding: '16px', borderRadius: '8px', background: bg, color, fontSize: '14px' }}>
        {message}
      </div>
      {status === 'error' && (
        <button
          onClick={() => navigate('/')}
          style={{ marginTop: '16px', padding: '8px 16px', fontSize: '13px', borderRadius: '6px', background: '#2563eb', color: '#fff', border: 'none', cursor: 'pointer' }}
        >
          Go to dashboard
        </button>
      )}
    </div>
  )
}
