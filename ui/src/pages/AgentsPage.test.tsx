/**
 * The Remote agents page (#266).
 *
 * The server enforces roles regardless; these check the page does not offer
 * what the server would refuse, that a revoked agent reads as revoked rather
 * than merely offline, and that the one-time registration command is shown in
 * full — it is the only time the token is ever visible.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

let role = 'admin'
vi.mock('@/store/authStore', () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ currentTenant: { id: 'tenant-1', user_role: role } }),
}))

const listAgents = vi.fn()
const createRegistrationToken = vi.fn()
vi.mock('@/services/agentService', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  listAgents: () => listAgents(),
  createRegistrationToken: (n?: string) => createRegistrationToken(n),
  renameAgent: vi.fn(),
  revokeAgent: vi.fn(),
}))

import AgentsPage from './AgentsPage'

const base = {
  tenant_id: 'tenant-1', hostname: 'LAGER-PC', os: 'windows', arch: 'amd64', agent_version: '0.1.0',
  registered_at: '2026-09-24T08:00:00Z',
}

beforeEach(() => {
  role = 'admin'
  listAgents.mockReset()
  createRegistrationToken.mockReset()
})

describe('AgentsPage', () => {
  it('shows online, offline and revoked agents distinctly, with their folders', async () => {
    listAgents.mockResolvedValue([
      { ...base, id: 'a1', name: 'till-1', online: true, last_seen_at: new Date().toISOString(),
        directories: [{ name: 'superpos-out', mode: 'read' }, { name: 'superpos-in', mode: 'write' }] },
      { ...base, id: 'a2', name: 'till-2', online: false, last_seen_at: null, directories: [] },
      { ...base, id: 'a3', name: 'old-pc', online: false, revoked_at: '2026-09-20T00:00:00Z', directories: [] },
    ])
    render(<AgentsPage />)

    expect(await screen.findByText('till-1')).toBeInTheDocument()
    expect(screen.getByText('Online')).toBeInTheDocument()
    expect(screen.getByText('Offline')).toBeInTheDocument()
    expect(screen.getByText('Revoked')).toBeInTheDocument()
    expect(screen.getByText('superpos-out · in')).toBeInTheDocument()
    expect(screen.getByText('superpos-in · out')).toBeInTheDocument()
    // A revoked agent offers no actions.
    expect(screen.getAllByRole('button', { name: 'Revoke' })).toHaveLength(2)
  })

  it('shows the full register command once, after minting a token', async () => {
    listAgents.mockResolvedValue([])
    createRegistrationToken.mockResolvedValue({
      id: 't1', tenant_id: 'tenant-1', token: 'vrsky_reg_abc123', expires_at: '2026-09-24T10:00:00Z',
    })
    render(<AgentsPage />)

    fireEvent.change(await screen.findByLabelText(/Name for the new agent/), { target: { value: ' LAGER-01 ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Generate registration token' }))

    await waitFor(() => expect(createRegistrationToken).toHaveBeenCalledWith('LAGER-01'))
    expect(await screen.findByText(`vrsky-agent register --url ${window.location.origin} --token vrsky_reg_abc123`))
      .toBeInTheDocument()
    expect(screen.getByText(/will not be shown again/)).toBeInTheDocument()
  })

  it('offers an editor rename but not token minting or revoke', async () => {
    role = 'editor'
    listAgents.mockResolvedValue([{ ...base, id: 'a1', name: 'till-1', online: true, directories: [] }])
    render(<AgentsPage />)

    expect(await screen.findByRole('button', { name: 'Rename' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Revoke' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Generate registration token' })).toBeNull()
  })

  it('offers a viewer nothing to change', async () => {
    role = 'viewer'
    listAgents.mockResolvedValue([{ ...base, id: 'a1', name: 'till-1', online: true, directories: [] }])
    render(<AgentsPage />)

    await screen.findByText('till-1')
    expect(screen.queryByRole('button', { name: 'Rename' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Revoke' })).toBeNull()
  })
})
