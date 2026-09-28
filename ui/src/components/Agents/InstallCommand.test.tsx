/**
 * The install command box on Settings → Remote agents (#266).
 *
 * The first real install (2026-09-28) went wrong three times on copy-paste of
 * the bare `vrsky-agent register …` line. These pin the one-liner's shape:
 * the origin and the token appear once each, as arguments.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

const getAgentRelease = vi.fn()
vi.mock('../../services/agentService', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  getAgentRelease: () => getAgentRelease(),
}))

import InstallCommand, { UninstallCommand } from './InstallCommand'
import { installCommand, uninstallCommand, agentDownloadUrl } from '../../services/agentService'

const origin = 'https://vrsky.example'
const token = 'vrsky_reg_0123abcd'

beforeEach(() => {
  getAgentRelease.mockReset()
  getAgentRelease.mockImplementation(() => Promise.resolve({
    platform: 'windows-amd64', version: 'b9548be', sha256: 'ab', size_bytes: 7, filename: 'vrsky-agent.exe',
  }))
})

describe('command strings', () => {
  it('builds the PowerShell one-liner with the origin and token once each, as arguments', () => {
    const cmd = installCommand(origin, token)
    expect(cmd).toBe(
      `& ([scriptblock]::Create((irm ${origin}/api/v1/agents/install.ps1))) -Url ${origin} -Token ${token}`,
    )
    expect(cmd.split(token).length - 1).toBe(1)
    expect(cmd.split('-Token').length - 1).toBe(1)
  })

  it('builds the uninstall one-liner and the download URL from the origin', () => {
    expect(uninstallCommand(origin)).toBe(`& ([scriptblock]::Create((irm ${origin}/api/v1/agents/uninstall.ps1)))`)
    expect(agentDownloadUrl(origin)).toBe(`${origin}/api/v1/agents/download/windows-amd64`)
  })
})

describe('InstallCommand', () => {
  it('shows the one-liner and copies exactly it', async () => {
    const writeText = vi.fn()
    Object.assign(navigator, { clipboard: { writeText } })
    render(<InstallCommand origin={origin} token={token} onDone={() => {}} />)

    const oneLiner = installCommand(origin, token)
    expect(screen.getByText(oneLiner)).toBeTruthy()
    fireEvent.click(screen.getAllByRole('button', { name: 'Copy' })[0])
    expect(writeText).toHaveBeenCalledWith(oneLiner)
    expect(await screen.findByText('Copied')).toBeTruthy()
    await screen.findByText(/version b9548be/) // let the release lookup land inside the test
  })

  it('offers the download with the server version, and the manual register line', async () => {
    render(<InstallCommand origin={origin} token={token} onDone={() => {}} />)
    const link = screen.getByRole('link', { name: 'Download vrsky-agent.exe' })
    expect(link.getAttribute('href')).toBe(`${origin}/api/v1/agents/download/windows-amd64`)
    await waitFor(() => expect(screen.getByText(/version b9548be/)).toBeTruthy())
    expect(screen.getByText(`vrsky-agent register --url ${origin} --token ${token}`)).toBeTruthy()
  })

  it('says so when this server has no agent build', async () => {
    getAgentRelease.mockImplementation(() => Promise.reject(new Error('404')))
    render(<InstallCommand origin={origin} token={token} onDone={() => {}} />)
    await waitFor(() => expect(screen.getByText(/built without the agent/)).toBeTruthy())
    expect(screen.queryByText(/version /)).toBeNull()
  })

  it('Done calls onDone', async () => {
    const onDone = vi.fn()
    render(<InstallCommand origin={origin} token={token} onDone={onDone} />)
    await screen.findByText(/version b9548be/)
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(onDone).toHaveBeenCalled()
  })
})

describe('UninstallCommand', () => {
  it('shows the uninstall one-liner for this origin', () => {
    render(<UninstallCommand origin={origin} />)
    expect(screen.getByText(uninstallCommand(origin))).toBeTruthy()
  })
})
