/**
 * #450: the Connections list against the API's real list shape.
 *
 * GET /api/v1/connections answers { data, total, limit, offset }
 * (src/pkg/managementapi/handler.go, ListResponse). The list must show the
 * connections' names from that body, and connectionService.list() must hand
 * them to its callers in `connections`. Only the HTTP client is mocked.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { connectionService } from '@/services/connectionService'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import ConnectionsList from './ConnectionsList'

const get = vi.mocked(apiClient.get)

const wireConnection = (id: string, name: string, status: string, created: string) => ({
  id,
  tenant_id: 'tenant-1',
  name,
  description: '',
  status,
  nodes: [
    { id: 'n1', type: 'consumer', config: { type: 'http' }, enabled: true },
    { id: 'n2', type: 'producer', config: { type: 'businesscentral' }, enabled: true },
  ],
  edges: [{ id: 'e1', source: 'n1', target: 'n2', order: 0 }],
  created_at: created,
  updated_at: '2026-10-28T13:26:01Z',
})

// The body the management API actually sends for the list endpoint.
const wireList = {
  data: [
    wireConnection('conn-1', 'HTTP→BusinessCentral', 'running', '2026-10-20T09:22:54Z'),
    wireConnection('conn-2', 'Pipeline 2:26:01 PM', 'running', '2026-10-21T14:26:01Z'),
  ],
  total: 2,
  limit: 10,
  offset: 0,
}

const listRoute = (body: unknown) =>
  ((url: string) => {
    if (/^\/api\/v1\/connections(\?|$)/.test(url)) return Promise.resolve({ data: body })
    return Promise.resolve({ data: {} })
  }) as never

beforeEach(() => {
  get.mockReset()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
})

const showList = () =>
  render(
    <MemoryRouter>
      <ConnectionsList />
    </MemoryRouter>,
  )

describe('ConnectionsList with the API wire shape', () => {
  it('shows every connection name from { data, total }', async () => {
    get.mockImplementation(listRoute(wireList))
    showList()

    expect(await screen.findByText('HTTP→BusinessCentral')).toBeInTheDocument()
    expect(screen.getByText('Pipeline 2:26:01 PM')).toBeInTheDocument()
    expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
    expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/connections\?/))
  })

  it('shows the current name after a rename (a reload sees the new name)', async () => {
    const renamed = {
      ...wireList,
      data: [{ ...wireList.data[0], name: 'BusinessCentral→RemoteAgent' }, wireList.data[1]],
    }
    get.mockImplementation(listRoute(renamed))
    showList()

    expect(await screen.findByText('BusinessCentral→RemoteAgent')).toBeInTheDocument()
    expect(screen.queryByText('HTTP→BusinessCentral')).not.toBeInTheDocument()
  })

  it('still says "No connections yet" for an empty workspace', async () => {
    get.mockImplementation(listRoute({ data: [], total: 0, limit: 10, offset: 0 }))
    showList()

    expect(await screen.findByText('No connections yet')).toBeInTheDocument()
  })
})

describe('connectionService.list with the API wire shape', () => {
  it('returns the items of `data` in `connections`, and the total', async () => {
    get.mockImplementation(listRoute(wireList))

    const result = await connectionService.list(1, 10)

    expect(result.total).toBe(2)
    expect(result.connections.map((c) => c.id)).toEqual(['conn-1', 'conn-2'])
    expect(result.connections.map((c) => c.name)).toEqual(['HTTP→BusinessCentral', 'Pipeline 2:26:01 PM'])
    expect(get).toHaveBeenCalledWith('/api/v1/connections?limit=10&offset=0')
  })

  it('keeps a response that already uses `connections`', async () => {
    const legacy = {
      connections: [wireConnection('conn-9', 'Legacy shape', 'stopped', '2026-10-01T00:00:00Z')],
      total: 1,
      page: 1,
      page_size: 20,
    }
    get.mockImplementation(listRoute(legacy))

    const result = await connectionService.list()

    expect(result.total).toBe(1)
    expect(result.connections.map((c) => c.name)).toEqual(['Legacy shape'])
  })
})
