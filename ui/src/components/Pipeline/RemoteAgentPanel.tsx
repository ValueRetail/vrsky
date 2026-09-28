/**
 * The builder's Remote Agent tab (#266): which agent or group and folder a
 * deployed pipeline uses, whether the agents are online, and live activity —
 * files received from a read folder, files written to a write folder,
 * failures, members skipped.
 *
 * Events come from the remote-agent gateway through the management API's
 * worker-events proxy (see services/workerEvents.ts). Online/offline comes from
 * the agent list, refreshed every 15 s: the gateway's stream says what moved,
 * not whether the machine is there.
 */

import { useEffect, useState } from 'react'
import { listAgents, resendConnection, type Agent } from '../../services/agentService'
import { subscribeWorkerEvents } from '../../services/workerEvents'
import type { RemoteAgentEnd } from './remoteAgentEnds'

export interface RemoteAgentEvent {
  type: string // connected | ingested | delivered | failed | warning
  agent?: string
  filename?: string
  envelope_id?: string
  message?: string
  time: string
}

const REFRESH_MS = 15_000

type Status = 'online' | 'offline' | 'revoked' | 'unknown'

/** What the header shows for one end: one agent's state, or a group's tally. */
type EndStatus =
  | { kind: 'agent'; status: Status }
  | { kind: 'group'; members: number; online: number; offline: string[] }

const statusStyle: Record<Status, { color: string; label: string }> = {
  online: { color: '#16a34a', label: 'online' },
  offline: { color: '#6b7280', label: 'offline' },
  revoked: { color: '#dc2626', label: 'revoked' },
  unknown: { color: '#9ca3af', label: 'not registered' },
}

function statusOf(end: RemoteAgentEnd, agents: Agent[]): EndStatus {
  if (end.group) {
    const members = agents.filter((a) => !a.revoked_at && (a.groups ?? []).includes(end.group!))
    return {
      kind: 'group',
      members: members.length,
      online: members.filter((a) => a.online).length,
      offline: members.filter((a) => !a.online).map((a) => a.name),
    }
  }
  const a = agents.find((x) => x.id === end.agentId)
  return { kind: 'agent', status: !a ? 'unknown' : a.revoked_at ? 'revoked' : a.online ? 'online' : 'offline' }
}

function describe(e: RemoteAgentEvent): { icon: string; color: string; text: string } {
  const to = e.agent ? ` → ${e.agent}` : ''
  switch (e.type) {
    case 'connected':
      return { icon: '●', color: '#6b7280', text: 'Listening for activity' }
    case 'ingested':
      return { icon: '↑', color: '#0891b2', text: `Received ${e.filename ?? 'a file'}${e.agent ? ` from ${e.agent}` : ''}` }
    case 'delivered':
      return { icon: '↓', color: '#16a34a', text: `Written ${e.filename ?? 'a file'}${to}` }
    case 'failed':
      return { icon: '✕', color: '#dc2626', text: (e.message || 'Failed') + to }
    case 'warning':
      return { icon: '!', color: '#d97706', text: e.message || 'Warning' }
    default:
      return { icon: '·', color: '#6b7280', text: e.type }
  }
}

interface Props {
  connectionId: string
  ends: RemoteAgentEnd[]
  /** The bottom panel hides inactive tabs rather than unmounting them, so the
   *  stream keeps collecting while another tab is shown. */
  visible: boolean
  onError?: (message: string) => void
}

export default function RemoteAgentPanel({ connectionId, ends, visible, onError }: Props) {
  const [events, setEvents] = useState<RemoteAgentEvent[]>([])
  const [status, setStatus] = useState<Record<string, EndStatus>>({})
  const [resent, setResent] = useState<string | null>(null)

  useEffect(() => {
    setEvents([])
    return subscribeWorkerEvents(connectionId, 'remote-agent', {
      onEvent: (e) => setEvents((prev) => [e as RemoteAgentEvent, ...prev].slice(0, 50)),
      onError,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectionId])

  const endsKey = ends.map((e) => `${e.role}:${e.group ?? e.agentId}`).join(',')
  useEffect(() => {
    let cancelled = false
    const refresh = () =>
      listAgents()
        .then((agents) => {
          if (cancelled) return
          const next: Record<string, EndStatus> = {}
          for (const end of ends) next[end.role] = statusOf(end, agents)
          setStatus(next)
        })
        .catch(() => {
          /* keep the last known status; the next refresh may succeed */
        })
    refresh()
    const t = setInterval(refresh, REFRESH_MS)
    return () => {
      cancelled = true
      clearInterval(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endsKey])

  const dest = ends.find((e) => e.role === 'destination')
  const destStatus = dest ? status[dest.role] : undefined
  const offlineNote =
    destStatus?.kind === 'agent' && destStatus.status === 'offline'
      ? 'The agent is offline. Files for it wait in VRSky and are written when it reconnects — up to 72 hours, 24 hours for files over 256 KB.'
      : destStatus?.kind === 'group' && destStatus.offline.length > 0
        ? `Offline: ${destStatus.offline.join(', ')}. Files for them wait in VRSky and are written when they reconnect — up to 72 hours, 24 hours for files over 256 KB. The others are not held up.`
        : null

  const resend = async () => {
    if (!window.confirm(
      'Send everything again?\n\nThe source re-sends all its data on its next poll (Business Central: all records and pictures). ' +
      'Every destination receives it again. Use this to give a till that just joined a group the full catalogue.',
    )) return
    try {
      await resendConnection(connectionId)
      setResent(new Date().toLocaleTimeString())
    } catch (e) {
      onError?.(e instanceof Error ? e.message : 'Resend failed')
    }
  }

  const headerFor = (end: RemoteAgentEnd) => {
    const s = status[end.role]
    if (!s) return <span style={{ color: '#9ca3af', fontWeight: 600 }}>…</span>
    if (s.kind === 'group') {
      const color = s.members === 0 ? '#dc2626' : s.online === s.members ? '#16a34a' : s.online > 0 ? '#d97706' : '#6b7280'
      return <span style={{ color, fontWeight: 600 }}>{s.online}/{s.members} online</span>
    }
    const st = statusStyle[s.status]
    return <span style={{ color: st.color, fontWeight: 600 }}>{st.label}</span>
  }

  return (
    <div
      className="bottom-tab-content"
      data-tab="agent"
      style={{ display: visible ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px', gap: '6px' }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '16px', alignItems: 'center', fontSize: '12px', flexShrink: 0 }}>
        {ends.map((end) => (
          <span key={end.role} data-testid={`agent-${end.role}`}>
            <span style={{ color: '#6b7280' }}>{end.role === 'source' ? 'Source' : 'Destination'}:</span>{' '}
            <span style={{ fontWeight: 600 }}>{end.group ? `group ${end.group}` : end.agentName}</span>
            <span style={{ color: '#6b7280' }}> · {end.directory} · </span>
            {headerFor(end)}
          </span>
        ))}
        <span style={{ marginLeft: 'auto', display: 'flex', gap: '8px', alignItems: 'center' }}>
          {resent && <span style={{ color: '#6b7280' }}>Resend requested at {resent}</span>}
          <button
            type="button"
            onClick={resend}
            title="Ask the source to send all its data again, e.g. to seed a till that just joined a group"
            style={{ padding: '3px 10px', fontSize: '11px', border: '1px solid #d1d5db', borderRadius: '4px', background: '#fff', cursor: 'pointer' }}
          >
            Resend everything
          </button>
        </span>
      </div>
      {offlineNote && (
        <div style={{ fontSize: '11px', color: '#374151', backgroundColor: '#f3f4f6', padding: '4px 8px', borderRadius: '4px', flexShrink: 0 }}>
          {offlineNote}
        </div>
      )}
      <div style={{ flex: 1, overflowY: 'auto', fontSize: '12px', fontFamily: 'monospace', backgroundColor: '#f9fafb', border: '1px solid #e5e7eb', borderRadius: '4px', padding: '6px 8px' }}>
        {events.length === 0 ? (
          <div style={{ color: '#9ca3af', textAlign: 'center', padding: '12px' }}>Waiting for agent activity...</div>
        ) : (
          events.map((e, i) => {
            const d = describe(e)
            return (
              <div key={i} data-testid="agent-event" style={{ display: 'flex', gap: '8px', padding: '2px 0' }}>
                <span style={{ color: '#9ca3af' }}>{new Date(e.time).toLocaleTimeString()}</span>
                <span style={{ color: d.color }}>{d.icon}</span>
                <span style={{ color: e.type === 'failed' || e.type === 'warning' ? d.color : '#374151' }}>{d.text}</span>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
