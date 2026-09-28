/**
 * The Remote Agent block of the node editor (#266).
 *
 * The editor chooses among what agents REPORTED — an agent, then one of its
 * folder names — never a path. These check it offers only live agents, only
 * folders of the right direction, and saves the IDs the gateway resolves.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

const listAgents = vi.fn()
const listAgentGroups = vi.fn()
vi.mock('../../services/agentService', () => ({
  listAgents: () => listAgents(),
  listAgentGroups: () => listAgentGroups(),
}))

import { useState } from 'react'
import RemoteAgentConfigEditor from './RemoteAgentConfigEditor'

const agents = [
  { id: 'a1', tenant_id: 't1', name: 'LAGER-01', hostname: 'lager', os: 'windows', arch: 'amd64', agent_version: '0.1.0',
    registered_at: '', online: true, groups: ['all-tills'],
    directories: [{ name: 'superpos-out', mode: 'read' }, { name: 'superpos-in', mode: 'write' }] },
  { id: 'a2', tenant_id: 't1', name: 'TILL-02', hostname: 'till', os: 'windows', arch: 'amd64', agent_version: '0.1.0',
    registered_at: '', online: false, groups: ['all-tills', 'store-oslo'], directories: [{ name: 'drop', mode: 'write' }] },
  { id: 'a3', tenant_id: 't1', name: 'OLD-PC', hostname: 'old', os: 'windows', arch: 'amd64', agent_version: '0.1.0',
    registered_at: '', online: false, revoked_at: '2026-09-01T00:00:00Z', groups: ['all-tills'], directories: [] },
]

// Renders the block the way PropertyEditor does: it owns the config state and
// hands the block setConfig. onChange sees every config the block produces.
function renderNode(type: 'input' | 'output', remote: Record<string, unknown> = {}) {
  const onChange = vi.fn()
  function Host() {
    const [config, setConfig] = useState<Record<string, unknown>>({ type: 'remote_agent', remote_agent: remote })
    return (
      <RemoteAgentConfigEditor
        config={config}
        setConfig={(c) => { setConfig(c); onChange(c) }}
        nodeType={type}
      />
    )
  }
  render(<Host />)
  return onChange
}

const optionLabels = (select: HTMLElement) =>
  Array.from((select as HTMLSelectElement).options).map((o) => o.textContent)

// Lazy implementations throughout: combining mockResolvedValue here with a
// rejecting mockImplementation in a test surfaced the rejection as unhandled.
beforeEach(() => {
  listAgents.mockReset()
  listAgents.mockImplementation(() => Promise.resolve(agents))
  listAgentGroups.mockReset()
  listAgentGroups.mockImplementation(() => Promise.resolve([
    { name: 'all-tills', members: 2, online: 1 },
    { name: 'store-oslo', members: 1, online: 0 },
  ]))
})

describe('Remote Agent node editor', () => {
  it('offers live agents with their state, never revoked ones', async () => {
    renderNode('output')
    const agent = await screen.findByLabelText('Agent')
    expect(optionLabels(agent)).toEqual(['Select an agent...', 'LAGER-01 · online', 'TILL-02 · offline'])
  })

  it('an output offers only write folders, and records the agent and folder IDs', async () => {
    const onChange = renderNode('output')
    fireEvent.change(await screen.findByLabelText('Agent'), { target: { value: 'a1' } })
    const folder = await screen.findByLabelText('Folder to write into')
    expect(optionLabels(folder)).toEqual(['Select a folder...', 'superpos-in'])
    fireEvent.change(folder, { target: { value: 'superpos-in' } })

    await waitFor(() => expect(onChange).toHaveBeenCalledTimes(2))
    expect(onChange.mock.calls[1][0]).toMatchObject({
      type: 'remote_agent',
      remote_agent: { agent_id: 'a1', agent_name: 'LAGER-01', directory: 'superpos-in' },
    })
  })

  it('an input offers only read folders, and what to do with a file afterwards', async () => {
    renderNode('input')
    fireEvent.change(await screen.findByLabelText('Agent'), { target: { value: 'a1' } })
    expect(optionLabels(await screen.findByLabelText('Folder to watch'))).toEqual(['Select a folder...', 'superpos-out'])
    const after = screen.getByLabelText('After a file is taken')
    expect(optionLabels(after)).toEqual(['Move it into processed/', 'Delete it'])
  })

  it('offers no agents to choose when there are none, and says where to register one', async () => {
    listAgents.mockImplementation(() => Promise.resolve([]))
    renderNode('output')
    expect(await screen.findByText(/No remote agents yet/)).toBeInTheDocument()
  })

  it('tells the user when an agent has no folders of the needed kind', async () => {
    renderNode('input', { agent_id: 'a2', agent_name: 'TILL-02' })
    expect(await screen.findByText(/TILL-02 has no read folders/)).toBeInTheDocument()
  })

  it('reports a failure to load agents instead of an empty list', async () => {
    listAgents.mockImplementation(() => Promise.reject(new Error('network down')))
    renderNode('output')
    expect(await screen.findByText('network down')).toBeInTheDocument()
  })

  it('says so when a chosen agent is offline, and when a saved agent is gone', async () => {
    renderNode('output', { agent_id: 'a2', agent_name: 'TILL-02', directory: 'drop' })
    expect(await screen.findByText(/TILL-02 is offline\. Files wait in VRSky/)).toBeInTheDocument()
  })

  it('flags a saved agent that is no longer registered', async () => {
    renderNode('output', { agent_id: 'deleted', agent_name: 'GONE-PC', directory: 'x' })
    expect(await screen.findByText(/GONE-PC\) is no longer registered/)).toBeInTheDocument()
  })

  // Groups: one node, many tills.
  it('can target a group: offers the groups with counts, and the folders its members report', async () => {
    const onChange = renderNode('output')
    fireEvent.change(await screen.findByLabelText('Send to'), { target: { value: 'group' } })
    const group = await screen.findByLabelText('Group')
    expect(optionLabels(group)).toEqual([
      'Select a group...', 'all-tills · 2 agents · 1 online', 'store-oslo · 1 agent · 0 online',
    ])
    fireEvent.change(group, { target: { value: 'all-tills' } })
    const folder = await screen.findByLabelText('Folder to write into')
    // Every member's write folders, marked when not every member has one.
    expect(optionLabels(folder)).toEqual([
      'Select a folder...', 'drop · only 1 of 2 agents have it', 'superpos-in · only 1 of 2 agents have it',
    ])
    fireEvent.change(folder, { target: { value: 'superpos-in' } })

    await waitFor(() => expect(onChange).toHaveBeenCalledTimes(3))
    const saved = onChange.mock.calls[2][0].remote_agent as Record<string, unknown>
    expect(saved).toMatchObject({ target: 'group', group: 'all-tills', directory: 'superpos-in' })
    expect(saved.agent_id).toBeUndefined()
    // The offline member is named, and the others are said not to wait for it.
    expect(screen.getByText(/TILL-02 is offline/)).toBeInTheDocument()
    expect(screen.getByText(/the others are not held up/)).toBeInTheDocument()
  })

  it('an input from a group offers the members\' read folders and the after option', async () => {
    renderNode('input', { target: 'group', group: 'all-tills' })
    const folder = await screen.findByLabelText('Folder to watch')
    expect(optionLabels(folder)).toEqual(['Select a folder...', 'superpos-out · only 1 of 2 agents have it'])
    expect(optionLabels(screen.getByLabelText('After a file is taken'))).toEqual(['Move it into processed/', 'Delete it'])
    expect(screen.queryByLabelText('Agent')).toBeNull()
  })

  it('warns when a saved group has no agents any more, and offers groups from the agents when the list fails', async () => {
    listAgentGroups.mockImplementation(() => Promise.reject(new Error('down')))
    renderNode('output', { target: 'group', group: 'ghost', directory: 'x' })
    expect(await screen.findByText(/No agent is in group "ghost" any more/)).toBeInTheDocument()
    // The agents themselves carry their groups, so the select still works.
    expect(optionLabels(screen.getByLabelText('Group'))).toEqual([
      'Select a group...', 'all-tills · 2 agents · 1 online', 'store-oslo · 1 agent · 0 online',
    ])
  })

  it('switching a node from an agent to a group drops the agent', async () => {
    const onChange = renderNode('output', { agent_id: 'a1', agent_name: 'LAGER-01', directory: 'superpos-in' })
    fireEvent.change(await screen.findByLabelText('Send to'), { target: { value: 'group' } })
    await waitFor(() => expect(onChange).toHaveBeenCalled())
    const saved = onChange.mock.calls[0][0].remote_agent as Record<string, unknown>
    expect(saved.target).toBe('group')
    expect(saved.agent_id).toBeUndefined()
    expect(saved.agent_name).toBeUndefined()
    expect(saved.directory).toBe('')
  })

  it('switching back to one agent drops the group', async () => {
    const onChange = renderNode('output', { target: 'group', group: 'all-tills', directory: 'superpos-in' })
    fireEvent.change(await screen.findByLabelText('Send to'), { target: { value: 'agent' } })
    await waitFor(() => expect(onChange).toHaveBeenCalled())
    const saved = onChange.mock.calls[0][0].remote_agent as Record<string, unknown>
    expect(saved.target).toBeUndefined()
    expect(saved.group).toBeUndefined()
    expect(saved.directory).toBe('')
  })
})
