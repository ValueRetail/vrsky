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
import { listMembers, setMemberRole, removeMember, type TenantMember, type TenantRole } from '@/services/membersService'

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
    </div>
  )
}
