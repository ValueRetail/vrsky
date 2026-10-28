/**
 * #450: the header's connection badge counts the workspace's connections from
 * the API, on any page under the layout, and follows a workspace switch.
 * Only the HTTP client is mocked; the stores and components are real.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, act } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useConnectionsStore } from '@/store/connectionsStore'
import { useUIStore } from '@/store/uiStore'
import type { Tenant, User } from '@/types/models'
import RootLayout from './RootLayout'

const get = vi.mocked(apiClient.get)

const tenant = (id: string, name: string): Tenant => ({
  id,
  name,
  slug: id,
  owner_id: 'user-1',
  subscription_plan: 'paid',
  is_verified: true,
  user_role: 'owner',
  max_integrations: 10,
  max_messages_per_month: 100000,
  status: 'active',
  billing_status: 'paid',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
})

const user: User = {
  id: 'user-1',
  email: 'owner@example.test',
  full_name: 'Owner',
  status: 'active',
  email_verified: true,
  created_at: '2026-10-01T00:00:00Z',
}

const t1 = tenant('tenant-1', 'Production')
const t2 = tenant('tenant-2', 'Sandbox')

const wireConnection = (id: string, tenantId: string, name: string, status: string) => ({
  id,
  tenant_id: tenantId,
  name,
  status,
  nodes: [],
  edges: [],
  created_at: '2026-10-20T09:22:54Z',
  updated_at: '2026-10-28T13:26:01Z',
})

// The list body per workspace, in the API's real shape ({ data, total, ... }).
let lists: Record<string, { data: unknown[]; total: number }> = {}

// The X-Tenant-ID header is added by the real client's interceptor, which the
// mock replaces; the workspace a request is for is the one current when it is
// made.
const route = ((url: string) => {
  if (/^\/api\/v1\/connections(\?|$)/.test(url)) {
    const id = useAuthStore.getState().currentTenant?.id ?? ''
    const list = lists[id] ?? { data: [], total: 0 }
    return Promise.resolve({ data: { ...list, limit: 100, offset: 0 } })
  }
  // Anything else the layout might ask for (plan, usage, ...) answers empty.
  return Promise.resolve({ data: {} })
}) as never

beforeEach(() => {
  get.mockReset()
  get.mockImplementation(route)
  useConnectionsStore.setState(useConnectionsStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
  useAuthStore.setState(
    {
      ...useAuthStore.getInitialState(),
      user,
      isAuthenticated: true,
      isInitialized: true,
      tenants: [t1, t2],
      currentTenant: t1,
    },
    true,
  )
  lists = {
    'tenant-1': {
      data: [
        wireConnection('conn-1', 'tenant-1', 'HTTP→BusinessCentral', 'running'),
        wireConnection('conn-2', 'tenant-1', 'Pipeline 2:26:01 PM', 'running'),
      ],
      total: 2,
    },
    'tenant-2': { data: [], total: 0 },
  }
})

const showLayout = () =>
  render(
    <MemoryRouter initialEntries={['/somewhere']}>
      <Routes>
        <Route element={<RootLayout />}>
          <Route path="/somewhere" element={<div>Dummy page</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )

// The counter is the element holding the number next to the "connections"
// label in the header.
const badge = () => screen.getByText('connections').parentElement as HTMLElement

describe('header connection badge', () => {
  it('shows the workspace connection count without visiting any page first', async () => {
    showLayout()
    expect(screen.getByText('Dummy page')).toBeInTheDocument()

    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*2\s*connections\s*$/))
    expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/connections(\?|$)/))
  })

  it('follows a workspace switch', async () => {
    showLayout()
    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*2\s*connections\s*$/))

    fireEvent.change(screen.getByRole('combobox', { name: 'Select workspace' }), {
      target: { value: 'tenant-2' },
    })
    expect(useAuthStore.getState().currentTenant?.id).toBe('tenant-2')

    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*0\s*connections\s*$/))
  })

  it('follows a switch to a workspace with a different non-zero count', async () => {
    lists['tenant-2'] = {
      data: [
        wireConnection('conn-3', 'tenant-2', 'a', 'running'),
        wireConnection('conn-4', 'tenant-2', 'b', 'stopped'),
        wireConnection('conn-5', 'tenant-2', 'c', 'error'),
      ],
      total: 3,
    }
    showLayout()
    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*2\s*connections\s*$/))

    fireEvent.change(screen.getByRole('combobox', { name: 'Select workspace' }), {
      target: { value: 'tenant-2' },
    })

    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*3\s*connections\s*$/))
  })

  it('counts the API total, not just the page of items it returned', async () => {
    lists['tenant-1'] = {
      data: [wireConnection('conn-1', 'tenant-1', 'HTTP→BusinessCentral', 'running')],
      total: 25,
    }
    showLayout()

    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*25\s*connections\s*$/))
  })
})

describe('header connection badge never shows stale counts', () => {
  const listCalls = () => get.mock.calls.filter(([url]) => /^\/api\/v1\/connections(\?|$)/.test(String(url)))

  it('without a workspace it shows 0 and asks for nothing, whatever the store held before', async () => {
    // Left over from a previous user in the same tab.
    useConnectionsStore.setState({
      connections: lists['tenant-1'].data as never,
      total: 5,
    })
    useAuthStore.setState({ currentTenant: null })

    showLayout()

    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*0\s*connections\s*$/))
    expect(screen.queryByText('Running')).not.toBeInTheDocument()
    expect(listCalls()).toHaveLength(0)
  })

  it('shows 0, not the old workspace count, while the new workspace loads', async () => {
    showLayout()
    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*2\s*connections\s*$/))
    expect(screen.getByText('Running')).toBeInTheDocument()

    // The next list request (for tenant-2) hangs until the test answers it.
    let answer!: (body: unknown) => void
    get.mockImplementationOnce((() => new Promise((resolve) => { answer = resolve })) as never)

    fireEvent.change(screen.getByRole('combobox', { name: 'Select workspace' }), {
      target: { value: 'tenant-2' },
    })
    await waitFor(() => expect(listCalls()).toHaveLength(2))

    expect(badge()).toHaveTextContent(/^\s*0\s*connections\s*$/)
    expect(screen.queryByText('Running')).not.toBeInTheDocument()

    await act(async () => {
      answer({
        data: {
          data: [wireConnection('conn-3', 'tenant-2', 'a', 'stopped'), wireConnection('conn-4', 'tenant-2', 'b', 'stopped')],
          total: 7,
          limit: 100,
          offset: 0,
        },
      })
    })
    await waitFor(() => expect(badge()).toHaveTextContent(/^\s*7\s*connections\s*$/))
    expect(screen.getByText('Stopped')).toBeInTheDocument()
  })
})
