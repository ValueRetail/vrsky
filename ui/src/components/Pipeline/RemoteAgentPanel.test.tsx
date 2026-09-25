/**
 * The builder's Remote Agent tab (#266): shows each agent end with its live
 * status, and the gateway's events as they arrive through the proxy.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'

const listAgents = vi.fn()
vi.mock('../../services/agentService', () => ({ listAgents: () => listAgents() }))

type Handlers = { onEvent: (e: unknown) => void; onError?: (m: string) => void }
const subscribe = vi.fn()
const unsubscribe = vi.fn()
vi.mock('../../services/workerEvents', () => ({
  subscribeWorkerEvents: (id: string, worker: string, h: Handlers) => {
    subscribe(id, worker, h)
    return unsubscribe
  },
}))

import RemoteAgentPanel from './RemoteAgentPanel'
import { remoteAgentDetail, remoteAgentEnds, type RemoteAgentEnd } from './remoteAgentEnds'

const agent = (id: string, extra: Record<string, unknown>) => ({
  id, tenant_id: 't1', name: id, hostname: '', os: 'windows', arch: 'amd64', agent_version: '0.1.0',
  registered_at: '', directories: [], online: false, ...extra,
})

const ends: RemoteAgentEnd[] = [
  { role: 'source', agentId: 'a1', agentName: 'LAGER-01', directory: 'superpos-out' },
  { role: 'destination', agentId: 'a2', agentName: 'TILL-02', directory: 'superpos-in' },
]

// Lets the first agent-list refresh land inside act().
const flush = () => act(async () => { await Promise.resolve() })

const handlers = (): Handlers => subscribe.mock.calls[subscribe.mock.calls.length - 1][2]

beforeEach(() => {
  listAgents.mockReset()
  subscribe.mockReset()
  unsubscribe.mockReset()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('remoteAgentEnds / remoteAgentDetail', () => {
  const node = (agentId: string, directory: string) => ({
    type: 'remote_agent', remote_agent: { agent_id: agentId, agent_name: `N-${agentId}`, directory },
  })

  it('returns the remote-agent ends, source first, and skips other types', () => {
    expect(remoteAgentEnds(node('a1', 'in'), node('a2', 'out'))).toEqual([
      { role: 'source', agentId: 'a1', agentName: 'N-a1', directory: 'in' },
      { role: 'destination', agentId: 'a2', agentName: 'N-a2', directory: 'out' },
    ])
    expect(remoteAgentEnds({ type: 'http' }, node('a2', 'out')).map((e) => e.role)).toEqual(['destination'])
    expect(remoteAgentEnds(undefined, { type: 'file' })).toEqual([])
  })

  it('ignores a remote-agent node with no agent picked yet', () => {
    expect(remoteAgentEnds({ type: 'remote_agent', remote_agent: {} }, undefined)).toEqual([])
    expect(remoteAgentEnds({ type: 'remote_agent' }, undefined)).toEqual([])
  })

  it('falls back to the agent ID when no display name was saved', () => {
    expect(remoteAgentEnds({ type: 'remote_agent', remote_agent: { agent_id: 'a9' } }, undefined)[0])
      .toEqual({ role: 'source', agentId: 'a9', agentName: 'a9', directory: '' })
  })

  it('formats the deployment detail as agent:folder', () => {
    expect(remoteAgentDetail(node('a1', 'superpos-out'))).toBe('N-a1:superpos-out')
    expect(remoteAgentDetail({ type: 'file' })).toBe('')
  })
})

describe('RemoteAgentPanel', () => {
  it('subscribes to the remote-agent stream for the connection and unsubscribes on unmount', async () => {
    listAgents.mockImplementation(() => Promise.resolve([]))
    const { unmount } = render(<RemoteAgentPanel connectionId="c1" ends={ends} visible />)
    await flush()
    expect(subscribe).toHaveBeenCalledWith('c1', 'remote-agent', expect.anything())
    unmount()
    expect(unsubscribe).toHaveBeenCalled()
  })

  it('shows each end with its agent, folder and status', async () => {
    listAgents.mockImplementation(() =>
      Promise.resolve([agent('a1', { online: true }), agent('a2', { online: false })]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends} visible />)

    await waitFor(() => expect(screen.getByTestId('agent-source').textContent).toContain('online'))
    expect(screen.getByTestId('agent-source').textContent).toContain('LAGER-01')
    expect(screen.getByTestId('agent-source').textContent).toContain('superpos-out')
    expect(screen.getByTestId('agent-destination').textContent).toContain('offline')
    // A destination agent that is offline: say what happens to its files.
    expect(screen.getByText(/wait in VRSky/)).toBeTruthy()
  })

  it('marks revoked and unregistered agents, and no offline note for them', async () => {
    listAgents.mockImplementation(() =>
      Promise.resolve([agent('a2', { revoked_at: '2026-09-01T00:00:00Z' })]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends} visible />)

    await waitFor(() => expect(screen.getByTestId('agent-destination').textContent).toContain('revoked'))
    expect(screen.getByTestId('agent-source').textContent).toContain('not registered')
    expect(screen.queryByText(/wait in VRSky/)).toBeNull()
  })

  it('does not show the offline note when only the source is offline', async () => {
    listAgents.mockImplementation(() =>
      Promise.resolve([agent('a1', { online: false }), agent('a2', { online: true })]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends} visible />)
    await waitFor(() => expect(screen.getByTestId('agent-destination').textContent).toContain('online'))
    expect(screen.queryByText(/wait in VRSky/)).toBeNull()
  })

  it('refreshes the status every 15 seconds', async () => {
    vi.useFakeTimers()
    listAgents.mockImplementation(() => Promise.resolve([agent('a1', { online: true })]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends.slice(0, 1)} visible />)
    await act(async () => { await Promise.resolve() })
    expect(screen.getByTestId('agent-source').textContent).toContain('online')

    listAgents.mockImplementation(() => Promise.resolve([agent('a1', { online: false })]))
    await act(async () => { vi.advanceTimersByTime(15_000); await Promise.resolve() })
    expect(screen.getByTestId('agent-source').textContent).toContain('offline')
    expect(listAgents).toHaveBeenCalledTimes(2)
  })

  it('keeps the last status when a refresh fails', async () => {
    vi.useFakeTimers()
    listAgents.mockImplementation(() => Promise.resolve([agent('a1', { online: true })]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends.slice(0, 1)} visible />)
    await act(async () => { await Promise.resolve() })

    listAgents.mockImplementation(() => Promise.reject(new Error('network')))
    await act(async () => { vi.advanceTimersByTime(15_000); await Promise.resolve() })
    expect(screen.getByTestId('agent-source').textContent).toContain('online')
  })

  it('lists events newest first, describing each type', async () => {
    listAgents.mockImplementation(() => Promise.resolve([]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends} visible />)
    await flush()
    expect(screen.getByText(/Waiting for agent activity/)).toBeTruthy()

    const t = '2026-09-24T10:00:00Z'
    act(() => {
      handlers().onEvent({ type: 'connected', time: t })
      handlers().onEvent({ type: 'ingested', filename: 'sales.csv', time: t })
      handlers().onEvent({ type: 'delivered', filename: 'orders.csv', time: t })
      handlers().onEvent({ type: 'failed', message: 'no folder named superpos-in', time: t })
    })

    const rows = screen.getAllByTestId('agent-event').map((r) => r.textContent)
    expect(rows).toHaveLength(4)
    expect(rows[0]).toContain('no folder named superpos-in')
    expect(rows[1]).toContain('Written orders.csv')
    expect(rows[2]).toContain('Received sales.csv')
    expect(rows[3]).toContain('Listening for activity')
  })

  it('keeps at most 50 events', async () => {
    listAgents.mockImplementation(() => Promise.resolve([]))
    render(<RemoteAgentPanel connectionId="c1" ends={ends} visible />)
    await flush()
    act(() => {
      for (let i = 0; i < 60; i++) handlers().onEvent({ type: 'delivered', filename: `f${i}`, time: '2026-09-24T10:00:00Z' })
    })
    const rows = screen.getAllByTestId('agent-event')
    expect(rows).toHaveLength(50)
    expect(rows[0].textContent).toContain('f59')
  })

  it('passes stream errors to onError', async () => {
    listAgents.mockImplementation(() => Promise.resolve([]))
    const onError = vi.fn()
    render(<RemoteAgentPanel connectionId="c1" ends={ends} visible onError={onError} />)
    await flush()
    handlers().onError?.('cannot reach the remote-agent service')
    expect(onError).toHaveBeenCalledWith('cannot reach the remote-agent service')
  })

  it('stays mounted but hidden when another tab is active', async () => {
    listAgents.mockImplementation(() => Promise.resolve([]))
    const { container } = render(<RemoteAgentPanel connectionId="c1" ends={ends} visible={false} />)
    await flush()
    expect((container.firstChild as HTMLElement).style.display).toBe('none')
    expect(subscribe).toHaveBeenCalled()
  })
})
