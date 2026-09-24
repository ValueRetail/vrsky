/**
 * The builder's Remote Agent tab (#266): which agent and folder a deployed
 * pipeline uses, whether that agent is online, and its live activity — files
 * received from a read folder, files written to a write folder, failures.
 *
 * Events come from the remote-agent gateway through the management API's
 * worker-events proxy (see services/workerEvents.ts). Online/offline comes from
 * the agent list, refreshed every 15 s: the gateway's stream says what moved,
 * not whether the machine is there.
 */

import { useEffect, useState } from 'react'
import { listAgents } from '../../services/agentService'
import { subscribeWorkerEvents } from '../../services/workerEvents'
import type { RemoteAgentEnd } from './remoteAgentEnds'

export interface RemoteAgentEvent {
  type: string // connected | ingested | delivered | failed
  filename?: string
  envelope_id?: string
  message?: string
  time: string
}

const REFRESH_MS = 15_000

type Status = 'online' | 'offline' | 'revoked' | 'unknown'

const statusStyle: Record<Status, { color: string; label: string }> = {
  online: { color: '#16a34a', label: 'online' },
  offline: { color: '#6b7280', label: 'offline' },
  revoked: { color: '#dc2626', label: 'revoked' },
  unknown: { color: '#9ca3af', label: 'not registered' },
}

function describe(e: RemoteAgentEvent): { icon: string; color: string; text: string } {
  switch (e.type) {
    case 'connected':
      return { icon: '●', color: '#6b7280', text: 'Listening for activity' }
    case 'ingested':
      return { icon: '↑', color: '#0891b2', text: `Received ${e.filename ?? 'a file'}` }
    case 'delivered':
      return { icon: '↓', color: '#16a34a', text: `Written ${e.filename ?? 'a file'}` }
    case 'failed':
      return { icon: '✕', color: '#dc2626', text: e.message || 'Failed' }
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
  const [status, setStatus] = useState<Record<string, Status>>({})

  useEffect(() => {
    setEvents([])
    return subscribeWorkerEvents(connectionId, 'remote-agent', {
      onEvent: (e) => setEvents((prev) => [e as RemoteAgentEvent, ...prev].slice(0, 50)),
      onError,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectionId])

  const agentIds = ends.map((e) => e.agentId).join(',')
  useEffect(() => {
    let cancelled = false
    const refresh = () =>
      listAgents()
        .then((agents) => {
          if (cancelled) return
          const next: Record<string, Status> = {}
          for (const id of agentIds.split(',')) {
            const a = agents.find((x) => x.id === id)
            next[id] = !a ? 'unknown' : a.revoked_at ? 'revoked' : a.online ? 'online' : 'offline'
          }
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
  }, [agentIds])

  const anyOffline = ends.some((e) => e.role === 'destination' && status[e.agentId] === 'offline')

  return (
    <div
      className="bottom-tab-content"
      data-tab="agent"
      style={{ display: visible ? 'flex' : 'none', flexDirection: 'column', height: '100%', padding: '8px 16px', gap: '6px' }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '16px', fontSize: '12px', flexShrink: 0 }}>
        {ends.map((end) => {
          const s = statusStyle[status[end.agentId] ?? 'unknown']
          return (
            <span key={end.role} data-testid={`agent-${end.role}`}>
              <span style={{ color: '#6b7280' }}>{end.role === 'source' ? 'Source' : 'Destination'}:</span>{' '}
              <span style={{ fontWeight: 600 }}>{end.agentName}</span>
              <span style={{ color: '#6b7280' }}> · {end.directory} · </span>
              <span style={{ color: s.color, fontWeight: 600 }}>{status[end.agentId] ? s.label : '…'}</span>
            </span>
          )
        })}
      </div>
      {anyOffline && (
        <div style={{ fontSize: '11px', color: '#374151', backgroundColor: '#f3f4f6', padding: '4px 8px', borderRadius: '4px', flexShrink: 0 }}>
          The agent is offline. Files for it wait in VRSky and are written when it reconnects — up to 72 hours,
          24 hours for files over 256 KB.
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
                <span style={{ color: e.type === 'failed' ? d.color : '#374151' }}>{d.text}</span>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
