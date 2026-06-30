/**
 * Members management page — Phase 1D (#69).
 *
 * Lists every (user, role) tuple for the active workspace. Owners can
 * change roles and remove members; the UI mirrors the server-side
 * "last owner can't leave" rule by disabling the relevant controls when
 * the row is the sole owner.
 */

import { useEffect, useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import {
  listMembers, setMemberRole, removeMember,
  inviteMember, listInvites, resendInvite, revokeInvite, inviteLink,
  type TenantMember, type TenantRole, type TenantInvite,
} from '@/services/membersService'

const ROLES: TenantRole[] = ['viewer', 'editor', 'admin', 'owner']

const cell: React.CSSProperties = {
  padding: '10px 12px',
  borderBottom: '1px solid #f3f4f6',
  fontSize: '13px',
  verticalAlign: 'middle',
}
const headerCell: React.CSSProperties = { ...cell, fontWeight: 600, background: '#f9fafb' }

export default function UsersPage() {
  const { currentTenant, user: me } = useAuthStore()
  const [members, setMembers] = useState<TenantMember[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [myRole, setMyRole] = useState<TenantRole | null>(null)
  const [addEmail, setAddEmail] = useState('')
  const [addRole, setAddRole] = useState<TenantRole>('viewer')
  const [adding, setAdding] = useState(false)
  const [addNotice, setAddNotice] = useState<string | null>(null)
  const [invites, setInvites] = useState<TenantInvite[]>([])
  const [lastLink, setLastLink] = useState<string | null>(null)

  const refresh = async () => {
    if (!currentTenant) {
      setLoading(false)
      return
    }
    setLoading(true)
    setError(null)
    try {
      const data = await listMembers(currentTenant.id)
      setMembers(data)
      const meInList = data.find((m) => m.user_id === me?.id)
      setMyRole(meInList ? meInList.role : null)
      // Pending invites are owner-only; ignore a 403 for non-owners.
      try {
        const inv = await listInvites(currentTenant.id)
        setInvites(inv.filter((i) => i.status === 'pending'))
      } catch {
        setInvites([])
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load members')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentTenant?.id])

  const ownerCount = members.filter((m) => m.role === 'owner').length
  const canMutate = myRole === 'owner'

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!currentTenant || !addEmail.trim()) return
    setAdding(true)
    setError(null)
    setAddNotice(null)
    setLastLink(null)
    try {
      const res = await inviteMember(currentTenant.id, addEmail.trim(), addRole)
      const email = addEmail.trim()
      setAddEmail('')
      setAddRole('viewer')
      if (res.added) {
        setAddNotice(`Added ${res.added.email} as ${res.added.role} (they already had an account).`)
      } else if (res.invite) {
        setAddNotice(`Invited ${email} as ${res.invite.role}. Share the invite link below — they join after signing up.`)
        if (res.invite.token) setLastLink(inviteLink(res.invite.token))
      }
      await refresh()
    } catch (err) {
      const ax = err as { response?: { data?: { error?: { message?: string }; message?: string } } }
      const msg = ax.response?.data?.error?.message || ax.response?.data?.message ||
        (err instanceof Error ? err.message : 'Failed to invite member')
      setError(msg)
    } finally {
      setAdding(false)
    }
  }

  const handleResend = async (inv: TenantInvite) => {
    setBusy(inv.id)
    setError(null)
    try {
      const updated = await resendInvite(inv.tenant_id, inv.id)
      if (updated.token) setLastLink(inviteLink(updated.token))
      setAddNotice(`Resent invite to ${inv.email}. A fresh link is below.`)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Resend failed')
    } finally {
      setBusy(null)
    }
  }

  const handleRevoke = async (inv: TenantInvite) => {
    if (!window.confirm(`Revoke the invite for ${inv.email}?`)) return
    setBusy(inv.id)
    setError(null)
    try {
      await revokeInvite(inv.tenant_id, inv.id)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Revoke failed')
    } finally {
      setBusy(null)
    }
  }

  const handleRoleChange = async (member: TenantMember, role: TenantRole) => {
    if (role === member.role) return
    if (!window.confirm(`Change ${member.email}'s role from ${member.role} to ${role}?`)) return
    setBusy(member.user_id)
    setError(null)
    try {
      await setMemberRole(member.tenant_id, member.user_id, role)
      await refresh()
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Role change failed'
      setError(msg)
    } finally {
      setBusy(null)
    }
  }

  const handleRemove = async (member: TenantMember) => {
    if (!window.confirm(`Remove ${member.email} from this workspace?`)) return
    setBusy(member.user_id)
    setError(null)
    try {
      await removeMember(member.tenant_id, member.user_id)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Removal failed')
    } finally {
      setBusy(null)
    }
  }

  return (
    <div style={{ padding: '20px', maxWidth: '1100px', margin: '0 auto' }}>
      <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '6px' }}>Members</h1>
      <p style={{ fontSize: '13px', color: '#6b7280', marginBottom: '20px' }}>
        Roles control what each member can do.{' '}
        <strong>Viewer</strong> reads only · <strong>Editor</strong> creates and deploys pipelines ·{' '}
        <strong>Admin</strong> deletes resources and manages SSO · <strong>Owner</strong> manages members and billing.
      </p>

      {!canMutate && (
        <div style={{ padding: '10px', background: '#fef3c7', color: '#78350f', borderRadius: '6px', fontSize: '12px', marginBottom: '12px' }}>
          Only owners can change roles or remove members. Your role is <strong>{myRole || 'none'}</strong>.
        </div>
      )}

      {error && (
        <div style={{ padding: '10px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>
          {error}
        </div>
      )}

      {addNotice && (
        <div style={{ padding: '10px', background: '#ecfdf5', color: '#065f46', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>
          {addNotice}
        </div>
      )}

      {lastLink && (
        <div style={{ padding: '10px', background: '#eff6ff', color: '#1e3a8a', fontSize: '12px', borderRadius: '6px', marginBottom: '12px', display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={{ fontWeight: 600 }}>Invite link:</span>
          <code style={{ flex: '1 1 320px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{lastLink}</code>
          <button
            onClick={() => navigator.clipboard?.writeText(lastLink)}
            style={{ padding: '4px 10px', fontSize: '12px', borderRadius: '4px', background: '#2563eb', color: '#fff', border: 'none', cursor: 'pointer' }}
          >
            Copy
          </button>
        </div>
      )}

      {/* Add or invite a member by email (#130). If the email already has a VRSky
          account they're added immediately; otherwise a pending invite is created
          and shown below until accepted. */}
      {canMutate && (
        <form
          onSubmit={handleAdd}
          style={{ display: 'flex', gap: '8px', alignItems: 'flex-end', flexWrap: 'wrap', marginBottom: '16px', padding: '12px', background: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '8px' }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', flex: '1 1 260px' }}>
            <label style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }}>Add member by email</label>
            <input
              type="email"
              required
              value={addEmail}
              onChange={(e) => setAddEmail(e.target.value)}
              placeholder="teammate@example.com"
              style={{ padding: '6px 10px', fontSize: '13px', borderRadius: '4px', border: '1px solid #d1d5db' }}
            />
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
            <label style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }}>Role</label>
            <select
              value={addRole}
              onChange={(e) => setAddRole(e.target.value as TenantRole)}
              style={{ padding: '6px 10px', fontSize: '13px', borderRadius: '4px', border: '1px solid #d1d5db' }}
            >
              {ROLES.map((r) => (
                <option key={r} value={r}>{r}</option>
              ))}
            </select>
          </div>
          <button
            type="submit"
            disabled={adding || !addEmail.trim()}
            style={{
              padding: '7px 16px', fontSize: '13px', fontWeight: 600, borderRadius: '4px',
              background: '#2563eb', color: '#fff', border: 'none',
              opacity: adding || !addEmail.trim() ? 0.5 : 1,
              cursor: adding || !addEmail.trim() ? 'not-allowed' : 'pointer',
            }}
          >
            {adding ? 'Sending…' : 'Add / invite'}
          </button>
          <span style={{ fontSize: '11px', color: '#9ca3af', flexBasis: '100%' }}>
            If they already have a VRSky account they're added immediately; otherwise we create a pending invite.
          </span>
        </form>
      )}

      <div style={{ background: '#fff', borderRadius: '8px', border: '1px solid #e5e7eb', overflow: 'hidden' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr>
              <th style={headerCell}>Email</th>
              <th style={headerCell}>Name</th>
              <th style={headerCell}>Role</th>
              <th style={headerCell}>Joined</th>
              <th style={headerCell}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr><td colSpan={5} style={{ ...cell, textAlign: 'center', color: '#6b7280' }}>Loading…</td></tr>
            )}
            {!loading && members.length === 0 && (
              <tr><td colSpan={5} style={{ ...cell, textAlign: 'center', color: '#6b7280' }}>No members.</td></tr>
            )}
            {members.map((m) => {
              const isSelf = m.user_id === me?.id
              const isLastOwner = m.role === 'owner' && ownerCount <= 1
              return (
                <tr key={m.user_id}>
                  <td style={cell}>{m.email}</td>
                  <td style={cell}>{m.full_name || '—'}</td>
                  <td style={cell}>
                    <select
                      value={m.role}
                      disabled={!canMutate || busy === m.user_id || isLastOwner}
                      onChange={(e) => handleRoleChange(m, e.target.value as TenantRole)}
                      style={{ padding: '4px 8px', fontSize: '12px', borderRadius: '4px' }}
                      title={isLastOwner ? 'Cannot demote the last owner — promote another owner first' : ''}
                    >
                      {ROLES.map((r) => (
                        <option key={r} value={r}>{r}</option>
                      ))}
                    </select>
                  </td>
                  <td style={cell}>{m.joined_at ? new Date(m.joined_at).toLocaleDateString() : '—'}</td>
                  <td style={cell}>
                    <button
                      onClick={() => handleRemove(m)}
                      disabled={!canMutate || busy === m.user_id || isLastOwner || isSelf}
                      title={isLastOwner ? 'Cannot remove the last owner' : isSelf ? 'Cannot remove yourself' : ''}
                      style={{
                        padding: '4px 10px', fontSize: '12px', borderRadius: '4px',
                        background: '#dc2626', color: '#fff', border: 'none',
                        opacity: !canMutate || isLastOwner || isSelf ? 0.4 : 1,
                        cursor: !canMutate || isLastOwner || isSelf ? 'not-allowed' : 'pointer',
                      }}
                    >
                      Remove
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {/* Pending invites (#130) — emails invited that haven't signed up + accepted yet. */}
      {canMutate && invites.length > 0 && (
        <div style={{ marginTop: '24px' }}>
          <h2 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '8px' }}>
            Pending invites <span style={{ color: '#9ca3af', fontWeight: 400 }}>({invites.length})</span>
          </h2>
          <div style={{ background: '#fff', borderRadius: '8px', border: '1px solid #e5e7eb', overflow: 'hidden' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr>
                  <th style={headerCell}>Email</th>
                  <th style={headerCell}>Role</th>
                  <th style={headerCell}>Expires</th>
                  <th style={headerCell}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {invites.map((inv) => (
                  <tr key={inv.id}>
                    <td style={cell}>{inv.email}</td>
                    <td style={cell}>{inv.role}</td>
                    <td style={cell}>{inv.expires_at ? new Date(inv.expires_at).toLocaleDateString() : '—'}</td>
                    <td style={{ ...cell, display: 'flex', gap: '6px' }}>
                      <button
                        onClick={() => handleResend(inv)}
                        disabled={busy === inv.id}
                        style={{ padding: '4px 10px', fontSize: '12px', borderRadius: '4px', background: '#2563eb', color: '#fff', border: 'none', cursor: 'pointer', opacity: busy === inv.id ? 0.5 : 1 }}
                      >
                        Resend
                      </button>
                      <button
                        onClick={() => handleRevoke(inv)}
                        disabled={busy === inv.id}
                        style={{ padding: '4px 10px', fontSize: '12px', borderRadius: '4px', background: '#dc2626', color: '#fff', border: 'none', cursor: 'pointer', opacity: busy === inv.id ? 0.5 : 1 }}
                      >
                        Revoke
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
