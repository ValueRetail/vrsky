/**
 * Settings → API Key. Only "not found" means there is no key yet; any other
 * failure is shown as what it is. An expired session used to look exactly
 * like a missing key here.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { VRSkyAPIError } from '@/services/api'

// One stable object: the page's effect depends on the workspace, and a new
// object per render would re-run it forever.
const authState = { currentTenant: { id: 't1', name: 'WS' } }
vi.mock('@/store/authStore', () => ({
  useAuthStore: () => authState,
}))
const addNotification = vi.fn()
const showConfirmDialog = vi.fn()
const uiState = { addNotification, showConfirmDialog, hideConfirmDialog: vi.fn() }
vi.mock('@/store/uiStore', () => ({
  useUIStore: () => uiState,
}))
const getApiKey = vi.fn()
const rotateApiKey = vi.fn()
vi.mock('@/services/tenantDataService', () => ({
  getApiKey: (id: string) => getApiKey(id),
  rotateApiKey: (id: string) => rotateApiKey(id),
}))

import ApiKeyPage from './ApiKeyPage'

beforeEach(() => {
  addNotification.mockReset()
  showConfirmDialog.mockReset()
  getApiKey.mockReset()
  rotateApiKey.mockReset()
})

describe('ApiKeyPage', () => {
  it('a 404 is simply "no key yet": no error shown', async () => {
    getApiKey.mockRejectedValue(new VRSkyAPIError('no API key found for this workspace', 'NotFound', { status: 404 }))
    render(<ApiKeyPage />)
    expect(await screen.findByText(/No API key generated yet/)).toBeInTheDocument()
    expect(addNotification).not.toHaveBeenCalled()
  })

  it('any other failure is reported with the server message', async () => {
    getApiKey.mockRejectedValue(new VRSkyAPIError('invalid or expired session', 'Unauthorized', { status: 401 }))
    render(<ApiKeyPage />)
    await waitFor(() => expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'error', message: 'invalid or expired session' })))
  })

  it('a failed rotation says why', async () => {
    getApiKey.mockResolvedValue({ id: 'k1', tenant_id: 't1', is_active: true, created_at: '2026-10-01T00:00:00Z' })
    rotateApiKey.mockRejectedValue(new VRSkyAPIError('insufficient permissions', 'Forbidden', { status: 403 }))
    render(<ApiKeyPage />)
    await waitFor(() => expect(getApiKey).toHaveBeenCalled())
    const button = await screen.findByRole('button', { name: /Rotate|Generate/ })
    button.click()
    const { onConfirm } = showConfirmDialog.mock.calls[0][0] as { onConfirm: () => Promise<void> }
    await onConfirm()
    expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'error', message: 'Failed to rotate API key: insufficient permissions' }))
  })
})

describe('ApiKeyPage rotation', () => {
  it('shows the new key once and says so', async () => {
    getApiKey.mockResolvedValue({ id: 'k1', tenant_id: 't1', is_active: true, created_at: '2026-10-01T00:00:00Z' })
    rotateApiKey.mockResolvedValue({ id: 'k2', tenant_id: 't1', is_active: true, created_at: '2026-10-05T00:00:00Z', raw_key: 'vrsky_ws_0123456789abcdef' })
    render(<ApiKeyPage />)
    const button = await screen.findByRole('button', { name: /Rotate|Generate/ })
    button.click()
    expect(showConfirmDialog).toHaveBeenCalledWith(expect.objectContaining({ destructive: true }))
    const { onConfirm } = showConfirmDialog.mock.calls[0][0] as { onConfirm: () => Promise<void> }
    await onConfirm()
    expect(rotateApiKey).toHaveBeenCalledWith('t1')
    expect(await screen.findByText(/vrsky_ws_0123456789abcdef/)).toBeInTheDocument()
    expect(addNotification).toHaveBeenCalledWith(expect.objectContaining({ type: 'success', title: 'Key Rotated' }))
  })
})
