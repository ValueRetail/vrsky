/**
 * Settings → Notifications: the Teams target type (plans/monitoring-prod.md).
 * The server validates too; this checks the page offers Teams, asks for the
 * Workflows webhook URL as the secret, and refuses to save without it.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

let role = 'admin'
vi.mock('@/store/authStore', () => ({
  useAuthStore: (sel: (s: unknown) => unknown) => sel({ currentTenant: { id: 'tenant-1', user_role: role } }),
}))
const addNotification = vi.fn()
const showConfirmDialog = vi.fn()
const hideConfirmDialog = vi.fn()
vi.mock('@/store/uiStore', () => ({
  useUIStore: () => ({ addNotification, showConfirmDialog, hideConfirmDialog }),
}))
const listTargets = vi.fn()
const createTarget = vi.fn()
const updateTarget = vi.fn()
const deleteTarget = vi.fn()
const testTarget = vi.fn()
vi.mock('@/services/notificationService', () => ({
  listTargets: () => listTargets(),
  createTarget: (input: unknown) => createTarget(input),
  updateTarget: (id: string, input: unknown) => updateTarget(id, input),
  deleteTarget: (id: string) => deleteTarget(id),
  testTarget: (id: string) => testTarget(id),
}))

const teamsTarget = {
  id: 't1', tenant_id: 'tenant-1', name: 'ops-teams', type: 'teams' as const, enabled: true, has_secret: true,
  platform: true, min_severity: 'warning', created_at: '2026-10-01T08:00:00Z', updated_at: '2026-10-01T08:00:00Z',
}
const emailTarget = {
  id: 't2', tenant_id: 'tenant-1', name: 'ops-mail', type: 'email' as const, email: 'ops@example.com', enabled: false,
  has_secret: false, platform: false, created_at: '2026-10-01T08:00:00Z', updated_at: '2026-10-01T08:00:00Z',
}

import NotificationsPage from './NotificationsPage'

beforeEach(() => {
  role = 'admin'
  addNotification.mockReset()
  showConfirmDialog.mockReset()
  hideConfirmDialog.mockReset()
  listTargets.mockReset().mockResolvedValue([])
  createTarget.mockReset().mockResolvedValue(teamsTarget)
  updateTarget.mockReset().mockResolvedValue(teamsTarget)
  deleteTarget.mockReset().mockResolvedValue(undefined)
  testTarget.mockReset().mockResolvedValue({ ok: true })
})

describe('NotificationsPage — Teams target', () => {
  it('offers Teams and asks for the Workflows webhook URL', async () => {
    render(<NotificationsPage />)
    await waitFor(() => expect(listTargets).toHaveBeenCalled())
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'teams' } })
    expect(screen.getByLabelText('Workflows webhook URL')).toBeInTheDocument()
    expect(screen.queryByLabelText('Incoming webhook URL')).toBeNull()
  })

  it('refuses to save a Teams target without the URL, then sends it as the secret', async () => {
    render(<NotificationsPage />)
    await waitFor(() => expect(listTargets).toHaveBeenCalled())
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'ops-teams' } })
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'teams' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add target' }))
    expect(createTarget).not.toHaveBeenCalled()
    expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'error', title: 'Missing webhook URL' }))

    fireEvent.change(screen.getByLabelText('Workflows webhook URL'), { target: { value: 'https://prod-00.logic.azure.com/workflows/abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add target' }))
    await waitFor(() => expect(createTarget).toHaveBeenCalled())
    expect(createTarget).toHaveBeenCalledWith(expect.objectContaining({ type: 'teams', secret: 'https://prod-00.logic.azure.com/workflows/abc', name: 'ops-teams' }))
  })
})

describe('NotificationsPage — configured targets', () => {
  it('lists targets with type, platform and disabled badges, and the recipient for email', async () => {
    listTargets.mockResolvedValue([teamsTarget, emailTarget])
    render(<NotificationsPage />)
    expect(await screen.findByText('ops-teams')).toBeInTheDocument()
    expect(screen.getByText('teams')).toBeInTheDocument()
    expect(screen.getByText('platform')).toBeInTheDocument()
    expect(screen.getByText('disabled')).toBeInTheDocument()
    expect(screen.getByText('ops@example.com')).toBeInTheDocument()
    expect(screen.getByText(/min severity: warning/)).toBeInTheDocument()
  })

  it('says so when there are no targets, and viewers get no form or actions', async () => {
    role = 'viewer'
    render(<NotificationsPage />)
    expect(await screen.findByText(/alerts have nowhere to go/)).toBeInTheDocument()
    expect(screen.getByText(/Only workspace/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add target' })).toBeNull()
  })

  it('Test reports success or the server\'s reason, and is disabled on a disabled target', async () => {
    listTargets.mockResolvedValue([teamsTarget, emailTarget])
    render(<NotificationsPage />)
    await screen.findByText('ops-teams')
    const tests = screen.getAllByRole('button', { name: 'Test' })
    expect(tests[1]).toBeDisabled()
    fireEvent.click(tests[0])
    await waitFor(() => expect(testTarget).toHaveBeenCalledWith('t1'))
    expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'success', title: 'Test sent' }))

    testTarget.mockResolvedValue({ ok: false, error: 'teams: webhook returned 400' })
    fireEvent.click(tests[0])
    await waitFor(() => expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'error', message: 'teams: webhook returned 400' })))
  })

  it('Disable/Enable flips enabled and Delete asks first, then deletes', async () => {
    listTargets.mockResolvedValue([teamsTarget])
    render(<NotificationsPage />)
    await screen.findByText('ops-teams')
    fireEvent.click(screen.getByRole('button', { name: 'Disable' }))
    await waitFor(() => expect(updateTarget).toHaveBeenCalledWith('t1', expect.objectContaining({ enabled: false, type: 'teams' })))

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(deleteTarget).not.toHaveBeenCalled()
    expect(showConfirmDialog).toHaveBeenCalledWith(expect.objectContaining({ destructive: true }))
    const { onConfirm } = showConfirmDialog.mock.calls[0][0] as { onConfirm: () => Promise<void> }
    await onConfirm()
    expect(hideConfirmDialog).toHaveBeenCalled()
    expect(deleteTarget).toHaveBeenCalledWith('t1')
    expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ title: 'Deleted' }))
  })

  it('surfaces a failed load and a failed create', async () => {
    listTargets.mockRejectedValue(new Error('boom'))
    render(<NotificationsPage />)
    await waitFor(() => expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'error', message: 'boom' })))

    listTargets.mockResolvedValue([])
    createTarget.mockRejectedValue(new Error('409 duplicate'))
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'dup' } })
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'webhook' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add target' }))
    expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ title: 'Missing URL' }))
    fireEvent.change(screen.getByLabelText(/Destination URL|URL/), { target: { value: 'https://example.com/hook' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add target' }))
    await waitFor(() => expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'error', message: '409 duplicate' })))
  })
})
