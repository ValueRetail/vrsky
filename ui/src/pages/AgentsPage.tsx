/**
 * Remote agents (#266).
 *
 * An agent is a small program on a customer machine that connects out to VRSky
 * over HTTPS and makes that machine usable as a pipeline input or output. This
 * page lists the workspace's agents with their online state, lets editors rename
 * them, and lets admins mint a one-time registration token or revoke an agent.
 *
 * Roles mirror the server: minting and revoking are admin, renaming is editor.
 * The server enforces them regardless; the UI only hides what would be refused.
 */

import { useEffect, useState } from 'react'
import { useAuthStore } from '@/store/authStore'
import {
  listAgents, createRegistrationToken, renameAgent, revokeAgent, registerCommand,
  type Agent, type AgentRegistrationToken,
} from '@/services/agentService'

const cell: React.CSSProperties = {
  padding: '10px 12px',
  borderBottom: '1px solid #f3f4f6',
  fontSize: '13px',
  verticalAlign: 'middle',
}
const headerCell: React.CSSProperties = { ...cell, fontWeight: 600, background: '#f9fafb' }
const button: React.CSSProperties = {
  padding: '4px 10px', fontSize: '12px', borderRadius: '4px', border: '1px solid #d1d5db',
  background: '#fff', cursor: 'pointer',
}

const ROLE_RANK: Record<string, number> = { viewer: 1, editor: 2, admin: 3, owner: 4 }

function relativeTime(iso?: string | null): string {
  if (!iso) return 'never'
  const secs = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.round(secs / 60)}m ago`
  if (secs < 86400) return `${Math.round(secs / 3600)}h ago`
  return new Date(iso).toLocaleDateString()
}

function StatusBadge({ agent }: { agent: Agent }) {
  const [label, bg, fg] = agent.revoked_at
    ? ['Revoked', '#f3f4f6', '#6b7280']
    : agent.online
      ? ['Online', '#dcfce7', '#166534']
      : ['Offline', '#fee2e2', '#991b1b']
  return (
    <span style={{ padding: '2px 8px', borderRadius: '9999px', fontSize: '11px', fontWeight: 600, background: bg, color: fg }}>
      {label}
    </span>
  )
}

export default function AgentsPage() {
  const currentTenant = useAuthStore((s) => s.currentTenant)
  const rank = ROLE_RANK[currentTenant?.user_role ?? ''] ?? 0
  const canRename = rank >= ROLE_RANK.editor
  const canAdmin = rank >= ROLE_RANK.admin

  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [suggestedName, setSuggestedName] = useState('')
  const [minting, setMinting] = useState(false)
  const [newToken, setNewToken] = useState<AgentRegistrationToken | null>(null)
  const [editing, setEditing] = useState<{ id: string; name: string } | null>(null)

  const refresh = async () => {
    if (!currentTenant) {
      setLoading(false)
      return
    }
    setError(null)
    try {
      setAgents(await listAgents())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load agents')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    setLoading(true)
    refresh()
    // Online state changes on its own as agents come and go, so poll gently.
    const t = setInterval(refresh, 15_000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentTenant?.id])

  const handleMint = async (e: React.FormEvent) => {
    e.preventDefault()
    setMinting(true)
    setError(null)
    try {
      setNewToken(await createRegistrationToken(suggestedName.trim() || undefined))
      setSuggestedName('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not create a registration token')
    } finally {
      setMinting(false)
    }
  }

  const handleRename = async () => {
    if (!editing || !editing.name.trim()) return
    setBusy(editing.id)
    setError(null)
    try {
      await renameAgent(editing.id, editing.name.trim())
      setEditing(null)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Rename failed')
    } finally {
      setBusy(null)
    }
  }

  const handleRevoke = async (agent: Agent) => {
    if (!window.confirm(
      `Revoke "${agent.name}"?\n\nThe agent is disconnected on its next request and cannot reconnect. ` +
      `Pipelines that use it stop delivering to it. This cannot be undone — the machine has to be registered again.`,
    )) return
    setBusy(agent.id)
    setError(null)
    try {
      await revokeAgent(agent.id)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Revoke failed')
    } finally {
      setBusy(null)
    }
  }

  const command = newToken ? registerCommand(window.location.origin, newToken.token) : ''

  return (
    <div style={{ padding: '20px', maxWidth: '1100px', margin: '0 auto' }}>
      <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '6px' }}>Remote agents</h1>
      <p style={{ fontSize: '13px', color: '#6b7280', marginBottom: '20px' }}>
        An agent is a small program on another machine — a till, a warehouse PC, a customer's server — that
        connects out to VRSky and lets pipelines read files from it or write files to it. It needs no open
        firewall ports. Which folders it can use is set in the agent's own config file, on that machine.
      </p>

      {error && (
        <div style={{ padding: '10px', background: '#fef2f2', color: '#991b1b', fontSize: '13px', borderRadius: '6px', marginBottom: '12px' }}>
          {error}
        </div>
      )}

      {newToken && (
        <div style={{ padding: '12px', background: '#fefce8', border: '1px solid #fde68a', color: '#713f12', fontSize: '12px', borderRadius: '6px', marginBottom: '16px' }}>
          <div style={{ fontWeight: 600, marginBottom: '6px' }}>
            Run this on the machine within the next hour. It will not be shown again.
          </div>
          <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
            <code style={{ flex: '1 1 420px', padding: '6px 8px', background: '#fff', border: '1px solid #fde68a', borderRadius: '4px', wordBreak: 'break-all' }}>
              {command}
            </code>
            <button
              onClick={() => navigator.clipboard?.writeText(command)}
              style={{ ...button, background: '#2563eb', color: '#fff', border: 'none' }}
            >
              Copy
            </button>
            <button onClick={() => setNewToken(null)} style={button}>Done</button>
          </div>
          <div style={{ marginTop: '6px', color: '#92400e' }}>
            The token works once. Anyone who has it can register a machine in this workspace until it is used or expires.
          </div>
        </div>
      )}

      {canAdmin && !newToken && (
        <form
          onSubmit={handleMint}
          style={{ display: 'flex', gap: '8px', alignItems: 'flex-end', flexWrap: 'wrap', marginBottom: '16px', padding: '12px', background: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '8px' }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', flex: '1 1 260px' }}>
            <label style={{ fontSize: '12px', fontWeight: 600, color: '#374151' }} htmlFor="ag-name">
              Name for the new agent (optional)
            </label>
            <input
              id="ag-name"
              value={suggestedName}
              onChange={(e) => setSuggestedName(e.target.value)}
              placeholder="LAGER-SERVER-01 — defaults to the machine's hostname"
              maxLength={255}
              style={{ padding: '6px 10px', fontSize: '13px', borderRadius: '4px', border: '1px solid #d1d5db' }}
            />
          </div>
          <button
            type="submit"
            disabled={minting}
            style={{
              padding: '7px 16px', fontSize: '13px', fontWeight: 600, borderRadius: '4px',
              background: '#2563eb', color: '#fff', border: 'none',
              opacity: minting ? 0.5 : 1, cursor: minting ? 'not-allowed' : 'pointer',
            }}
          >
            {minting ? 'Creating…' : 'Generate registration token'}
          </button>
        </form>
      )}

      <div style={{ background: '#fff', borderRadius: '8px', border: '1px solid #e5e7eb', overflow: 'hidden' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr>
              <th style={headerCell}>Name</th>
              <th style={headerCell}>Status</th>
              <th style={headerCell}>Machine</th>
              <th style={headerCell}>Folders</th>
              <th style={headerCell}>Last seen</th>
              <th style={headerCell}></th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr><td style={cell} colSpan={6}>Loading…</td></tr>
            )}
            {!loading && agents.length === 0 && (
              <tr>
                <td style={{ ...cell, color: '#6b7280' }} colSpan={6}>
                  No agents yet.{canAdmin ? ' Generate a registration token above, then run the command it shows on the machine.' : ''}
                </td>
              </tr>
            )}
            {agents.map((a) => {
              const revoked = Boolean(a.revoked_at)
              const isEditing = editing?.id === a.id
              return (
                <tr key={a.id} style={{ opacity: revoked ? 0.55 : 1 }}>
                  <td style={cell}>
                    {isEditing ? (
                      <span style={{ display: 'flex', gap: '6px' }}>
                        <input
                          aria-label={`New name for ${a.name}`}
                          value={editing.name}
                          maxLength={255}
                          onChange={(e) => setEditing({ id: a.id, name: e.target.value })}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter') handleRename()
                            if (e.key === 'Escape') setEditing(null)
                          }}
                          style={{ padding: '4px 8px', fontSize: '13px', borderRadius: '4px', border: '1px solid #d1d5db' }}
                        />
                        <button onClick={handleRename} disabled={busy === a.id} style={button}>Save</button>
                        <button onClick={() => setEditing(null)} style={button}>Cancel</button>
                      </span>
                    ) : (
                      <strong>{a.name}</strong>
                    )}
                  </td>
                  <td style={cell}><StatusBadge agent={a} /></td>
                  <td style={{ ...cell, color: '#4b5563' }}>
                    {a.hostname || '—'}
                    <div style={{ fontSize: '11px', color: '#9ca3af' }}>
                      {[a.os, a.arch].filter(Boolean).join('/')}{a.agent_version ? ` · v${a.agent_version}` : ''}
                    </div>
                  </td>
                  <td style={cell}>
                    {a.directories.length === 0 ? (
                      <span style={{ color: '#9ca3af' }}>none reported</span>
                    ) : (
                      <span style={{ display: 'flex', gap: '4px', flexWrap: 'wrap' }}>
                        {a.directories.map((d) => (
                          <span key={d.name} title={d.mode === 'read' ? 'Pipelines read from this folder' : 'Pipelines write to this folder'}
                            style={{ padding: '1px 6px', borderRadius: '4px', fontSize: '11px', background: d.mode === 'read' ? '#eff6ff' : '#f0fdf4', color: d.mode === 'read' ? '#1e40af' : '#166534' }}>
                            {d.name} · {d.mode === 'read' ? 'in' : 'out'}
                          </span>
                        ))}
                      </span>
                    )}
                  </td>
                  <td style={{ ...cell, color: '#4b5563' }}>{relativeTime(a.last_seen_at)}</td>
                  <td style={{ ...cell, textAlign: 'right', whiteSpace: 'nowrap' }}>
                    {!revoked && canRename && !isEditing && (
                      <button onClick={() => setEditing({ id: a.id, name: a.name })} style={{ ...button, marginRight: '6px' }}>
                        Rename
                      </button>
                    )}
                    {!revoked && canAdmin && (
                      <button
                        onClick={() => handleRevoke(a)}
                        disabled={busy === a.id}
                        style={{ ...button, color: '#b91c1c', borderColor: '#fecaca' }}
                      >
                        Revoke
                      </button>
                    )}
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
