/**
 * The workspace selector in the header: which workspaces can be picked,
 * switching, deleting the current one behind a confirmation, and opening the
 * create dialog. Only HTTP is mocked: the API client's methods, and the
 * transport under the auth client that serves GET /api/v1/auth/me. Without a
 * session token the refresh after a delete makes no request; the tests that
 * store one see whether /me is asked for.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import type { AxiosAdapter } from 'axios'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

// The auth client (authService) is its own axios instance; its requests go
// to this transport instead of the network.
const transport = vi.hoisted(() => vi.fn<AxiosAdapter>())
vi.mock('axios', async (importOriginal) => {
  const actual = await importOriginal<typeof import('axios')>()
  const create: typeof actual.default.create = (config) =>
    actual.default.create({ ...config, adapter: (c) => transport(c) })
  return { ...actual, default: Object.assign(Object.create(actual.default), { create }) }
})

import apiClient, { VRSkyAPIError } from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import type { MeResponse, Tenant, TenantStatus, User } from '@/types/models'
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

const SESSION_KEY = 'vrsky_session_token'

const user: User = {
  id: 'user-1',
  email: 'owner@example.com',
  full_name: 'Owner',
  status: 'active',
  email_verified: true,
  created_at: '2026-10-01T00:00:00Z',
}

// What the auth client's transport answers for /me.
const meAnswers = (me: MeResponse) =>
  transport.mockImplementation(async (config) => ({
    status: 200, statusText: 'OK', data: me, headers: {}, config,
  }))

const meRequests = () => transport.mock.calls.filter(([c]) => c.url === '/api/v1/auth/me')

// Signed in with a live session: a checkAuth would ask /me.
const signInWithSession = (tenants: Tenant[]) => {
  sessionStorage.setItem(SESSION_KEY, 'test-session-token')
  useAuthStore.setState({ isAuthenticated: true, isInitialized: true, user, tenants, currentTenant: tenants[0] })
}

const authSnapshot = () => {
  const s = useAuthStore.getState()
  return {
    isAuthenticated: s.isAuthenticated,
    isInitialized: s.isInitialized,
    user: s.user,
    tenants: s.tenants,
    currentTenant: s.currentTenant,
  }
}

const confirmDelete = async () => {
  fireEvent.click(screen.getByTitle('Delete current workspace'))
  await act(async () => {
    await useUIStore.getState().confirmDialog!.onConfirm()
  })
}

const errors = () => useUIStore.getState().notifications.filter((n) => n.type === 'error')

beforeEach(() => {
  del.mockReset()
  transport.mockReset()
  transport.mockRejectedValue(new Error('unexpected request through the auth client'))
  sessionStorage.clear()
  localStorage.clear()
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

  it('a delete the API rejects says so once, with the API message', async () => {
    signIn([prod, sandbox])
    del.mockRejectedValue(new VRSkyAPIError('Only the workspace owner can delete it', 'FORBIDDEN', { status: 403 }))
    render(<TenantSelector />)
    await confirmDelete()
    expect(useUIStore.getState().notifications).toHaveLength(1)
    expect(errors()).toEqual([
      expect.objectContaining({ title: 'Workspace not deleted', message: 'Only the workspace owner can delete it' }),
    ])
  })

  it('an error with no message of its own gets the fallback text', async () => {
    signIn([prod, sandbox])
    del.mockRejectedValue({ status: 500 })
    render(<TenantSelector />)
    await confirmDelete()
    expect(errors()).toEqual([
      expect.objectContaining({
        title: 'Workspace not deleted',
        message: 'Could not delete the workspace. Try again.',
      }),
    ])
  })

  it('a failed delete leaves the session alone and never asks /me', async () => {
    signInWithSession([prod, sandbox])
    // If /me were asked it would answer with the workspace gone, so a
    // refresh would show in the state as well as in the requests.
    meAnswers({ user, session_expires_at: '2026-10-30T00:00:00Z', tenants: [sandbox], current_tenant: sandbox })
    del.mockRejectedValue(new VRSkyAPIError('Network request failed. Please check your connection.', 'NETWORK_ERROR'))
    render(<TenantSelector />)
    const before = authSnapshot()
    await confirmDelete()
    expect(meRequests()).toHaveLength(0)
    expect(authSnapshot()).toEqual(before)
    expect(sessionStorage.getItem(SESSION_KEY)).toBe('test-session-token')
    expect(select()).toHaveValue('tenant-1')
    expect(errors()).toHaveLength(1)
  })

  it('a successful delete refreshes through /me and shows no error', async () => {
    signInWithSession([prod, sandbox])
    meAnswers({ user, session_expires_at: '2026-10-30T00:00:00Z', tenants: [sandbox], current_tenant: sandbox })
    del.mockResolvedValue({ data: {} } as never)
    render(<TenantSelector />)
    await confirmDelete()
    expect(del).toHaveBeenCalledWith('/api/v1/tenants/tenant-1')
    expect(meRequests()).toHaveLength(1)
    expect(meRequests()[0][0].headers.Authorization).toBe('Bearer test-session-token')
    expect(del.mock.invocationCallOrder[0]).toBeLessThan(transport.mock.invocationCallOrder[0])
    expect(useAuthStore.getState().tenants.map((t) => t.id)).toEqual(['tenant-2'])
    expect(useAuthStore.getState().currentTenant?.id).toBe('tenant-2')
    expect(useAuthStore.getState().isAuthenticated).toBe(true)
    expect(errors()).toEqual([])
  })

  // #552: a workspace deleted in another tab or by another owner is still in
  // this selector. Deleting it again gets a 403 (no membership left) or a
  // 404 (no workspace left). The server answered, so a refresh through /me
  // is safe, and it drops the stale entry.
  describe('a delete the server refuses with 403 or 404', () => {
    const me = (tenants: Tenant[], current: Tenant): MeResponse => ({
      user, session_expires_at: '2026-10-30T00:00:00Z', tenants, current_tenant: current,
    })

    it.each([
      [403, 'Forbidden', 'you are not a member of this tenant'],
      [404, 'NotFound', 'tenant not found'],
    ])('%i: says so, refreshes, and drops a workspace that is gone', async (status, code, message) => {
      signInWithSession([prod, sandbox])
      localStorage.setItem('vrsky:selectedTenantId', 'tenant-1')
      meAnswers(me([sandbox], sandbox))
      del.mockRejectedValue(new VRSkyAPIError(message, code, { status, error: code, message }))
      render(<TenantSelector />)
      await confirmDelete()

      expect(errors()).toEqual([expect.objectContaining({ title: 'Workspace not deleted', message })])
      expect(meRequests()).toHaveLength(1)
      expect(del.mock.invocationCallOrder[0]).toBeLessThan(transport.mock.invocationCallOrder[0])
      const s = useAuthStore.getState()
      expect(s.isAuthenticated).toBe(true)
      expect(s.tenants.map((t) => t.id)).toEqual(['tenant-2'])
      expect(s.currentTenant?.id).toBe('tenant-2')
      expect(sessionStorage.getItem(SESSION_KEY)).toBe('test-session-token')
      expect(select()).toHaveValue('tenant-2')
      expect((screen.getAllByRole('option') as HTMLOptionElement[]).map((o) => o.value)).toEqual(['tenant-2'])
    })

    it('403 for a workspace that still exists (not the owner): refreshes, keeps it', async () => {
      signInWithSession([prod, sandbox])
      localStorage.setItem('vrsky:selectedTenantId', 'tenant-1')
      meAnswers(me([prod, sandbox], prod))
      del.mockRejectedValue(new VRSkyAPIError('insufficient permissions', 'Forbidden', { status: 403 }))
      render(<TenantSelector />)
      await confirmDelete()

      expect(errors()).toEqual([expect.objectContaining({ message: 'insufficient permissions' })])
      expect(meRequests()).toHaveLength(1)
      const s = useAuthStore.getState()
      expect(s.isAuthenticated).toBe(true)
      expect(s.tenants.map((t) => t.id)).toEqual(['tenant-1', 'tenant-2'])
      expect(s.currentTenant?.id).toBe('tenant-1')
      expect(select()).toHaveValue('tenant-1')
    })
  })

  describe('any other failed delete leaves the session and selector alone', () => {
    it.each<[string, unknown]>([
      ['a network error', new VRSkyAPIError('Network request failed. Please check your connection.', 'NETWORK_ERROR', { originalError: 'boom' })],
      ['a 401', new VRSkyAPIError('authentication required', 'Unauthorized', { status: 401 })],
      ['a 409', new VRSkyAPIError('workspace is busy', 'Conflict', { status: 409 })],
      ['a 500', new VRSkyAPIError('failed to delete workspace', 'ServerError', { status: 500 })],
      ['a 502', new VRSkyAPIError('HTTP Error 502', 'HTTP_502', { status: 502 })],
      ['a plain Error', new Error('HTTP Error 404')],
      ['a bare object', { status: 404 }],
    ])('%s: no /me request', async (_name, failure) => {
      signInWithSession([prod, sandbox])
      // /me would drop the workspace, so a refresh would show in the state.
      meAnswers({ user, session_expires_at: '2026-10-30T00:00:00Z', tenants: [sandbox], current_tenant: sandbox })
      del.mockRejectedValue(failure)
      render(<TenantSelector />)
      const before = authSnapshot()
      await confirmDelete()
      expect(meRequests()).toHaveLength(0)
      expect(authSnapshot()).toEqual(before)
      expect(select()).toHaveValue('tenant-1')
      expect(errors()).toHaveLength(1)
    })
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
