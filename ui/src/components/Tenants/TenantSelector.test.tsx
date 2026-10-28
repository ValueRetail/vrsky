/**
 * The workspace selector in the header: which workspaces can be picked,
 * switching, deleting the current one behind a confirmation, and opening the
 * create dialog. Only the HTTP client is mocked; no session token is stored,
 * so the refresh after a delete makes no request.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import type { Tenant, TenantStatus } from '@/types/models'
import TenantSelector from './TenantSelector'

const del = vi.mocked(apiClient.delete)

const tenant = (id: string, name: string, status: TenantStatus = 'active'): Tenant => ({
  id,
  name,
  slug: id,
  owner_id: 'user-1',
  subscription_plan: 'paid',
  is_verified: true,
  user_role: 'owner',
  max_integrations: 10,
  max_messages_per_month: 100000,
  status,
  billing_status: 'paid',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
})

const prod = tenant('tenant-1', 'Production')
const sandbox = tenant('tenant-2', 'Sandbox')
const pending = tenant('tenant-3', 'New one', 'provisioning')
const broken = tenant('tenant-4', 'Broken', 'failed')

const signIn = (tenants: Tenant[]) =>
  useAuthStore.setState({ isAuthenticated: true, isInitialized: true, tenants, currentTenant: tenants[0] })

beforeEach(() => {
  del.mockReset()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
})

const select = () => screen.getByRole('combobox', { name: 'Select workspace' })

describe('TenantSelector', () => {
  it('renders nothing without workspaces', () => {
    const { container } = render(<TenantSelector />)
    expect(container).toBeEmptyDOMElement()
  })

  it('lists every workspace with the role, and marks ones that are not ready', () => {
    signIn([prod, sandbox, pending, broken])
    render(<TenantSelector />)
    const options = screen.getAllByRole('option') as HTMLOptionElement[]
    expect(options.map((o) => o.textContent)).toEqual([
      'Production (owner)',
      'Sandbox (owner)',
      'New one (owner) - setting up...',
      'Broken (owner) - failed',
    ])
    expect(options.map((o) => o.disabled)).toEqual([false, false, true, true])
    expect(select()).toHaveValue('tenant-1')
  })

  it('switches to an active workspace', () => {
    signIn([prod, sandbox])
    render(<TenantSelector />)
    fireEvent.change(select(), { target: { value: 'tenant-2' } })
    expect(useAuthStore.getState().currentTenant?.id).toBe('tenant-2')
    expect(localStorage.getItem('vrsky:selectedTenantId')).toBe('tenant-2')
  })

  it('never switches to a workspace that is still setting up', () => {
    signIn([prod, pending])
    render(<TenantSelector />)
    fireEvent.change(select(), { target: { value: 'tenant-3' } })
    expect(useAuthStore.getState().currentTenant?.id).toBe('tenant-1')
  })

  it('offers delete only when there is another workspace to fall back to', () => {
    signIn([prod])
    render(<TenantSelector />)
    expect(screen.queryByTitle('Delete current workspace')).not.toBeInTheDocument()
  })

  it('deletes the current workspace after confirmation, then refreshes', async () => {
    signIn([prod, sandbox])
    del.mockResolvedValue({ data: {} } as never)
    render(<TenantSelector />)

    fireEvent.click(screen.getByTitle('Delete current workspace'))
    const dialog = useUIStore.getState().confirmDialog!
    expect(dialog).toMatchObject({ title: 'Delete Workspace', destructive: true })
    expect(dialog.message).toContain('"Production"')
    expect(del).not.toHaveBeenCalled()

    await act(async () => {
      await dialog.onConfirm()
    })
    expect(del).toHaveBeenCalledWith('/api/v1/tenants/tenant-1')
    expect(useUIStore.getState().confirmDialog).toBeNull()
    // The refresh ran: with no session left it signs the user out.
    expect(useAuthStore.getState().isInitialized).toBe(true)
    expect(useAuthStore.getState().tenants).toEqual([])
  })

  it('a failed delete closes the dialog and keeps the workspaces', async () => {
    signIn([prod, sandbox])
    del.mockRejectedValue(new Error('HTTP Error 409'))
    render(<TenantSelector />)
    fireEvent.click(screen.getByTitle('Delete current workspace'))
    await act(async () => {
      await useUIStore.getState().confirmDialog!.onConfirm()
    })
    expect(del).toHaveBeenCalledTimes(1)
    expect(useUIStore.getState().confirmDialog).toBeNull()
    expect(useAuthStore.getState().tenants.map((t) => t.id)).toEqual(['tenant-1', 'tenant-2'])
    expect(useAuthStore.getState().currentTenant?.id).toBe('tenant-1')
  })

  it('opens and closes the create dialog', () => {
    signIn([prod])
    render(<TenantSelector />)
    expect(screen.queryByText('Create new workspace')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTitle('Create new workspace'))
    expect(screen.getByRole('heading', { name: 'Create new workspace' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('heading', { name: 'Create new workspace' })).not.toBeInTheDocument()
  })
})
