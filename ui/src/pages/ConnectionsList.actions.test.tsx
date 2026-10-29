/**
 * What a user does on the Connections list: filter by status, page through,
 * stop, start and delete a connection, and what they see when a request
 * fails. After a change the header-store summary (badge, sidebar counts) is
 * refreshed from the API (#450). Only the HTTP client is mocked.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { MemoryRouter, Routes, Route, useParams } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient, { VRSkyAPIError } from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { useConnectionsStore } from '@/store/connectionsStore'
import ConnectionsList from './ConnectionsList'

const get = vi.mocked(apiClient.get)
const post = vi.mocked(apiClient.post)
const del = vi.mocked(apiClient.delete)

type Wire = Record<string, unknown> & { id: string; name: string; status: string }

const conn = (id: string, name: string, status: string, extra: Record<string, unknown> = {}): Wire => ({
  id,
  tenant_id: 'tenant-1',
  name,
  status,
  nodes: [
    { id: 'n1', type: 'consumer', config: { type: 'http' }, enabled: true },
    { id: 'n2', type: 'producer', config: { type: 'businesscentral' }, enabled: true },
  ],
  edges: [],
  created_at: '2026-10-20T09:22:54Z',
  updated_at: '2026-10-28T13:26:01Z',
  ...extra,
})

// What the server holds: the list (paged by limit/offset) and, per id, what
// GET /connections/:id answers after a start or stop.
let server: { items: Wire[]; total?: number; detail: Record<string, unknown> } = { items: [], detail: {} }

const route = ((url: string) => {
  const list = /^\/api\/v1\/connections\?(.*)$/.exec(url)
  if (list) {
    const q = new URLSearchParams(list[1])
    const limit = Number(q.get('limit') ?? 20)
    const offset = Number(q.get('offset') ?? 0)
    return Promise.resolve({
      data: {
        data: server.items.slice(offset, offset + limit),
        total: server.total ?? server.items.length,
        limit,
        offset,
      },
    })
  }
  const one = /^\/api\/v1\/connections\/([^/?]+)$/.exec(url)
  if (one) return Promise.resolve({ data: { data: server.detail[one[1]] ?? {} } })
  return Promise.resolve({ data: {} })
}) as never

beforeEach(() => {
  get.mockReset()
  post.mockReset()
  del.mockReset()
  get.mockImplementation(route)
  post.mockResolvedValue({ data: {} } as never)
  del.mockResolvedValue({ data: {} } as never)
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
  useConnectionsStore.setState(useConnectionsStore.getInitialState(), true)
  server = {
    items: [
      conn('conn-1', 'Orders in', 'running'),
      conn('conn-2', 'Stock out', 'stopped'),
      conn('conn-3', 'Broken one', 'error'),
    ],
    detail: {},
  }
})

const show = () =>
  render(
    <MemoryRouter>
      <ConnectionsList />
    </MemoryRouter>,
  )

const row = (name: string) => screen.getByText(name).closest('.card-elevated') as HTMLElement
const notifications = () => useUIStore.getState().notifications
const lastNotification = () => notifications()[notifications().length - 1]

describe('ConnectionsList: status filter', () => {
  it('shows only connections with the chosen status, and all again', async () => {
    show()
    await screen.findByText('Orders in')
    const filter = screen.getByLabelText('Status Filter:')

    fireEvent.change(filter, { target: { value: 'running' } })
    expect(screen.getByText('Orders in')).toBeInTheDocument()
    expect(screen.queryByText('Stock out')).not.toBeInTheDocument()
    expect(screen.queryByText('Broken one')).not.toBeInTheDocument()

    fireEvent.change(filter, { target: { value: 'stopped' } })
    expect(screen.getByText('Stock out')).toBeInTheDocument()
    expect(screen.queryByText('Orders in')).not.toBeInTheDocument()

    fireEvent.change(filter, { target: { value: 'error' } })
    expect(screen.getByText('Broken one')).toBeInTheDocument()
    expect(screen.queryByText('Stock out')).not.toBeInTheDocument()

    fireEvent.change(filter, { target: { value: 'all' } })
    for (const name of ['Orders in', 'Stock out', 'Broken one']) {
      expect(screen.getByText(name)).toBeInTheDocument()
    }
  })

  it('says "No connections yet" when the filter matches nothing', async () => {
    server.items = [conn('conn-1', 'Orders in', 'running')]
    show()
    await screen.findByText('Orders in')
    fireEvent.change(screen.getByLabelText('Status Filter:'), { target: { value: 'error' } })
    expect(screen.getByText('No connections yet')).toBeInTheDocument()
  })
})

describe('ConnectionsList: what a row shows', () => {
  it('shows source, destination, created date and description; a dash for what is missing', async () => {
    server.items = [
      conn('conn-1', 'Full', 'running', { description: 'Moves orders' }),
      conn('conn-2', 'Bare', 'stopped', {
        nodes: [{ id: 'n1', type: 'consumer', config: {}, enabled: true }],
        created_at: 'not a date',
      }),
      conn('conn-3', 'No nodes', 'stopped', { nodes: undefined, created_at: undefined }),
    ]
    show()
    await screen.findByText('Full')

    const full = row('Full')
    expect(within(full).getByText('Moves orders')).toBeInTheDocument()
    expect(full).toHaveTextContent('Source: http')
    expect(full).toHaveTextContent('Destination: businesscentral')
    expect(full).toHaveTextContent(`Created: ${new Date('2026-10-20T09:22:54Z').toLocaleDateString()}`)
    expect(within(full).getByText('Running')).toBeInTheDocument()

    // A consumer node without a config type falls back to the node type; no
    // producer, no date: dashes.
    const bare = row('Bare')
    expect(bare).toHaveTextContent('Source: consumer')
    expect(bare).toHaveTextContent('Destination: —')
    expect(bare).toHaveTextContent('Created: —')

    const none = row('No nodes')
    expect(none).toHaveTextContent('Source: —')
    expect(none).toHaveTextContent('Destination: —')
    expect(none).toHaveTextContent('Created: —')
  })

  it.each([
    ['View', 'Detail of conn-1'],
    ['Edit', 'Editor of conn-1'],
  ])('%s opens the connection page', async (button, page) => {
    server.items = [conn('conn-1', 'Orders in', 'running')]
    const Detail = () => <p>Detail of {useParams().id}</p>
    const Editor = () => <p>Editor of {useParams().id}</p>
    render(
      <MemoryRouter initialEntries={['/connections']}>
        <Routes>
          <Route path="/connections" element={<ConnectionsList />} />
          <Route path="/connections/:id" element={<Detail />} />
          <Route path="/connections/:id/edit" element={<Editor />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('Orders in')
    fireEvent.click(within(row('Orders in')).getByRole('button', { name: button }))
    expect(screen.getByText(page)).toBeInTheDocument()
  })
})

describe('ConnectionsList: pagination', () => {
  it('pages through with Next and Previous, asking for the right offset', async () => {
    server.items = Array.from({ length: 12 }, (_, i) => conn(`conn-${i + 1}`, `Pipe ${i + 1}`, 'running'))
    show()
    await screen.findByText('Pipe 1')
    expect(screen.getByText('Page 1 of 2')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('Pipe 11')).toBeInTheDocument()
    expect(get).toHaveBeenCalledWith('/api/v1/connections?limit=10&offset=10')
    expect(screen.getByText('Page 2 of 2')).toBeInTheDocument()
    expect(screen.queryByText('Pipe 1')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
    expect(await screen.findByText('Pipe 1')).toBeInTheDocument()
    expect(screen.getByText('Page 1 of 2')).toBeInTheDocument()
  })

  it('has no pagination for one page', async () => {
    show()
    await screen.findByText('Orders in')
    expect(screen.queryByRole('button', { name: 'Next' })).not.toBeInTheDocument()
  })
})

describe('ConnectionsList: load failure', () => {
  it('shows the API error message', async () => {
    get.mockRejectedValueOnce(new VRSkyAPIError('Workspace suspended', 'HTTP_402'))
    show()
    await waitFor(() => expect(lastNotification()).toMatchObject({ type: 'error', message: 'Workspace suspended' }))
    // An error state, not a claim that the workspace is empty (#515).
    expect(screen.getByText("Couldn't load connections")).toBeInTheDocument()
    expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
  })

  it('falls back to a generic message for a non-API error', async () => {
    get.mockRejectedValueOnce('boom')
    show()
    await waitFor(() => expect(lastNotification()).toMatchObject({ type: 'error', message: 'Failed to load connections' }))
  })
})

describe('ConnectionsList: stop and start', () => {
  it('Stop calls /stop, the row follows the server, and the summary is refreshed', async () => {
    show()
    await screen.findByText('Orders in')
    // The server now has it stopped, and a fourth connection appeared elsewhere.
    server.detail['conn-1'] = conn('conn-1', 'Orders in', 'stopped')
    server.total = 4

    fireEvent.click(within(row('Orders in')).getByRole('button', { name: 'Stop' }))

    await waitFor(() => expect(within(row('Orders in')).getByText('Stopped')).toBeInTheDocument())
    expect(post).toHaveBeenCalledWith('/api/v1/connections/conn-1/stop', {})
    expect(within(row('Orders in')).getByRole('button', { name: 'Start' })).toBeInTheDocument()
    expect(lastNotification()).toMatchObject({ type: 'success', title: 'Stopped' })
    await waitFor(() => expect(useConnectionsStore.getState().total).toBe(4))
  })

  it('Start calls /start and shows the status the server reports, not a guess', async () => {
    show()
    await screen.findByText('Stock out')
    server.detail['conn-2'] = conn('conn-2', 'Stock out', 'error')
    server.total = 3

    fireEvent.click(within(row('Stock out')).getByRole('button', { name: 'Start' }))

    await waitFor(() => expect(within(row('Stock out')).getByText('Error')).toBeInTheDocument())
    expect(post).toHaveBeenCalledWith('/api/v1/connections/conn-2/start', {})
    expect(lastNotification()).toMatchObject({ type: 'success', title: 'Started' })
    await waitFor(() => expect(useConnectionsStore.getState().total).toBe(3))
  })

  it('Start with no status in the answer shows the connection running', async () => {
    show()
    await screen.findByText('Stock out')
    fireEvent.click(within(row('Stock out')).getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(within(row('Stock out')).getByText('Running')).toBeInTheDocument())
  })

  it('Stop with no status in the answer shows the connection stopped', async () => {
    show()
    await screen.findByText('Orders in')
    fireEvent.click(within(row('Orders in')).getByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(within(row('Orders in')).getByText('Stopped')).toBeInTheDocument())
  })

  it.each([
    ['Stop', 'Orders in', 'Running', 'Failed to stop connection'],
    ['Start', 'Stock out', 'Stopped', 'Failed to start connection'],
  ])('a failed %s keeps the row and says why', async (button, name, status, fallback) => {
    show()
    await screen.findByText(name)

    post.mockRejectedValueOnce(new VRSkyAPIError('Worker unavailable', 'HTTP_503'))
    fireEvent.click(within(row(name)).getByRole('button', { name: button }))
    await waitFor(() => expect(lastNotification()).toMatchObject({ type: 'error', message: 'Worker unavailable' }))
    expect(within(row(name)).getByText(status)).toBeInTheDocument()

    post.mockRejectedValueOnce('boom')
    fireEvent.click(within(row(name)).getByRole('button', { name: button }))
    await waitFor(() => expect(lastNotification()).toMatchObject({ type: 'error', message: fallback }))
    await waitFor(() => expect(within(row(name)).getByRole('button', { name: button })).toBeEnabled())
  })
})

describe('ConnectionsList: delete', () => {
  it('asks first, then removes the row and refreshes the summary', async () => {
    show()
    await screen.findByText('Orders in')

    fireEvent.click(within(row('Orders in')).getByRole('button', { name: 'Delete' }))
    const dialog = useUIStore.getState().confirmDialog!
    expect(dialog).toMatchObject({ title: 'Delete Connection', destructive: true })
    expect(dialog.message).toContain('"Orders in"')
    expect(del).not.toHaveBeenCalled()

    server.items = server.items.filter((c) => c.id !== 'conn-1')
    await act(async () => {
      await dialog.onConfirm()
    })

    expect(del).toHaveBeenCalledWith('/api/v1/connections/conn-1')
    expect(useUIStore.getState().confirmDialog).toBeNull()
    expect(screen.queryByText('Orders in')).not.toBeInTheDocument()
    expect(screen.getByText('Stock out')).toBeInTheDocument()
    expect(lastNotification()).toMatchObject({ type: 'success', title: 'Deleted' })
    await waitFor(() => expect(useConnectionsStore.getState().total).toBe(2))
  })

  it('keeps the row when the delete fails', async () => {
    show()
    await screen.findByText('Orders in')
    fireEvent.click(within(row('Orders in')).getByRole('button', { name: 'Delete' }))

    del.mockRejectedValueOnce(new VRSkyAPIError('Not allowed', 'HTTP_403'))
    await act(async () => {
      await useUIStore.getState().confirmDialog!.onConfirm()
    })
    expect(lastNotification()).toMatchObject({ type: 'error', message: 'Not allowed' })
    expect(screen.getByText('Orders in')).toBeInTheDocument()

    fireEvent.click(within(row('Orders in')).getByRole('button', { name: 'Delete' }))
    del.mockRejectedValueOnce('boom')
    await act(async () => {
      await useUIStore.getState().confirmDialog!.onConfirm()
    })
    expect(lastNotification()).toMatchObject({ type: 'error', message: 'Failed to delete connection' })
    expect(screen.getByText('Orders in')).toBeInTheDocument()
  })
})
