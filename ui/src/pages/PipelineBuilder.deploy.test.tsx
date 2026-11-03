/**
 * #652: the builder page's Deploy button, end to end through the real page,
 * the real canvas, UI and auth stores and the real deploy service. Only what
 * leaves the page is replaced: the HTTP client, the live worker streams, and
 * the canvas widgets (Konva canvas, palette, property editor, panels), which
 * are stood in for by small components that pass the page's props on.
 *
 * The page itself is not in the coverage gate (ui/vitest.config.ts); this test
 * is what pins it. The deploy sequence's details are in the
 * services/deployPipeline tests.
 */

import { describe, it, expect, vi, beforeEach, beforeAll } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))
vi.mock('@/services/workerEvents', () => ({ subscribeWorkerEvents: vi.fn(() => () => {}) }))
vi.mock('@/services/dlqService', () => ({ listDLQ: vi.fn().mockResolvedValue([]) }))
vi.mock('@/components/Tenants/TenantSelector', () => ({ default: () => null }))
vi.mock('@/components/Pipeline/ComponentPalette', () => ({ default: () => null }))
vi.mock('@/components/Pipeline/DLQPanel', () => ({ default: () => null }))
vi.mock('@/components/Pipeline/RemoteAgentPanel', () => ({ default: () => null }))
vi.mock('@/components/CanvasSelector', () => ({ default: () => null }))

// The canvas shows a button per node; clicking one selects it, as a click on
// the Konva canvas does.
vi.mock('@/components/Pipeline/KonvaCanvas', () => ({
  default: ({ nodes, onNodeSelect }: { nodes: Array<{ id: string; data: { label: string } }>; onNodeSelect: (id: string) => void }) => (
    <div>
      {nodes.map((n) => (
        <button key={n.id} onClick={() => onNodeSelect(n.id)}>{`node ${n.data.label}`}</button>
      ))}
    </div>
  ),
}))
// The property editor saves a fixed config for whichever node is selected.
vi.mock('@/components/Pipeline/PropertyEditor', () => ({
  default: ({ node, onUpdate }: { node: { type: string } | null; onUpdate: (c: Record<string, unknown>) => void }) =>
    node ? (
      <button
        onClick={() =>
          onUpdate(node.type === 'input' ? { type: 'http' } : { type: 'file', file: { path: '/data/out/orders.csv' } })
        }
      >
        Save node config
      </button>
    ) : null,
}))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useCanvasStore } from '@/store/canvasStore'
import { useUIStore } from '@/store/uiStore'
import type { Tenant } from '@/types/models'
import type { Canvas } from '@/types/pipeline'
import ConfirmDialog from '@/components/Common/ConfirmDialog'
import PipelineBuilder from './PipelineBuilder'

const post = vi.mocked(apiClient.post)
const put = vi.mocked(apiClient.put)

const tenant = { id: 'tenant-1', name: 'Production' } as Tenant
const CONN = 'c0ffee00-1111-2222-3333-444444444444'
const short = `${CONN.substring(0, 8)}...`

const canvas = (extra: Partial<Canvas> = {}, configured = false): Canvas => ({
  id: 'canvas-1',
  name: 'Orders',
  nodes: [
    { id: 'in-1', type: 'input', data: { label: 'Input 1', type: 'input', config: configured ? { type: 'http' } : {} }, position: { x: 0, y: 0 } },
    {
      id: 'out-1',
      type: 'output',
      data: { label: 'Output 1', type: 'output', config: configured ? { type: 'file', file: { path: '/data/out/orders.csv' } } : {} },
      position: { x: 200, y: 0 },
    },
  ],
  edges: [{ id: 'e1', source: 'in-1', target: 'out-1' }],
  createdAt: 1,
  updatedAt: 1,
  ...extra,
})

// The page takes its canvases from the persisted store, as it does after a reload.
const seed = (c: Canvas) =>
  localStorage.setItem('vrsky:canvases:v1:tenant-1', JSON.stringify({ canvases: [c], currentCanvasId: c.id }))

// App.tsx renders the confirm dialog next to the routes; so does the test.
function Dialog() {
  const { confirmDialog, hideConfirmDialog } = useUIStore()
  return <ConfirmDialog config={confirmDialog} onClose={hideConfirmDialog} />
}

const showBuilder = () =>
  render(
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="/" element={<PipelineBuilder />} />
      </Routes>
      <Dialog />
    </MemoryRouter>,
  )

const notices = () => useUIStore.getState().notifications
const calls = (fn: typeof post) => fn.mock.calls.map((c) => c[0])
const notFound = () => Object.assign(new Error('not found'), { response: { status: 404 } })

beforeAll(() => {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(console, 'log').mockImplementation(() => {})
  vi.spyOn(console, 'error').mockImplementation(() => {})
  localStorage.clear()
  useCanvasStore.setState(useCanvasStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
  useAuthStore.setState({ ...useAuthStore.getInitialState(), isAuthenticated: true, currentTenant: tenant }, true)
})

describe('PipelineBuilder deploy', () => {
  it('creates the connection, starts it and says it is running', async () => {
    seed(canvas({}, true))
    post.mockResolvedValueOnce({ data: { data: { id: CONN } } } as never).mockResolvedValueOnce({ data: {} } as never)
    showBuilder()

    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(calls(post)).toEqual(['/api/v1/connections', `/api/v1/connections/${CONN}/start`])
    const body = post.mock.calls[0][1] as { name: string; nodes: Array<{ id: string; type: string; config: unknown }>; edges: unknown[] }
    expect(body.name).toBe('Orders')
    expect(body.nodes.map((n) => [n.id, n.type])).toEqual([['in-1', 'consumer'], ['out-1', 'producer']])
    expect(body.nodes[1].config).toEqual({ type: 'file', file: { path: '/data/out/orders.csv' } })
    expect(body.edges).toEqual([{ id: 'e1', source: 'in-1', target: 'out-1', order: 0 }])
    expect(put).not.toHaveBeenCalled()

    expect(notices()).toHaveLength(1)
    expect(notices()[0]).toMatchObject({ type: 'success', title: 'Pipeline Started' })
    expect(notices()[0].message).toContain(`Pipeline ${short} deployed and running!`)
    expect(notices()[0].message).toContain(`/webhook/${CONN}`)
    expect(await screen.findByText(/DEPLOYED/)).toBeInTheDocument()
    // The canvas is now linked to the connection, so the next deploy updates it.
    expect(useCanvasStore.getState().canvases[0].deployedConnectionId).toBe(CONN)
  })

  it('updates a linked pipeline in place: stop, update, start, same id', async () => {
    seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
    post.mockResolvedValue({ data: {} } as never)
    put.mockResolvedValue({ data: {} } as never)
    showBuilder()

    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(calls(post)).toEqual([`/api/v1/connections/${CONN}/stop`, `/api/v1/connections/${CONN}/start`])
    expect(put).toHaveBeenCalledTimes(1)
    expect(put.mock.calls[0][0]).toBe(`/api/v1/connections/${CONN}`)
    expect(notices()[0]).toMatchObject({ type: 'success', title: 'Pipeline Started' })
    expect(notices()[0].message).toContain(`Pipeline ${short} deployed and running!`)
  })

  it('says the pipeline was saved but not started when the start fails', async () => {
    seed(canvas({}, true))
    post
      .mockResolvedValueOnce({ data: { data: { id: CONN } } } as never)
      .mockRejectedValueOnce(new Error('consumer image is not available'))
    showBuilder()

    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))

    await waitFor(() => expect(notices()).toHaveLength(1))
    expect(calls(post)).toEqual(['/api/v1/connections', `/api/v1/connections/${CONN}/start`])
    expect(notices()[0]).toMatchObject({ type: 'error', title: 'Pipeline saved, not started' })
    expect(notices()[0].message).toContain('consumer image is not available')
    expect(notices().some((n) => n.type === 'success')).toBe(false)
    expect(await screen.findByText(/SAVED, NOT STARTED/)).toBeInTheDocument()
    expect(screen.queryByText(/DEPLOYED/)).not.toBeInTheDocument()
  })

  it('reports a failed create as a failed deploy and never tries to start', async () => {
    seed(canvas({}, true))
    post.mockRejectedValueOnce(new Error('integration count quota reached'))
    showBuilder()

    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))

    await waitFor(() => expect(notices()).toHaveLength(1))
    expect(notices()[0]).toMatchObject({ type: 'error', title: 'Deployment failed', message: 'integration count quota reached' })
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('deploys only after the nodes are configured through the editor', async () => {
    seed(canvas())
    post.mockResolvedValueOnce({ data: { data: { id: CONN } } } as never).mockResolvedValueOnce({ data: {} } as never)
    showBuilder()

    fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
    expect(notices()[0]).toMatchObject({ type: 'error', title: 'Pipeline validation', message: 'Configure Input node before deploying' })
    expect(post).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'node Input 1' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save node config' }))
    fireEvent.click(screen.getByRole('button', { name: 'node Output 1' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save node config' }))
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(calls(post)).toEqual(['/api/v1/connections', `/api/v1/connections/${CONN}/start`])
    await waitFor(() => expect(notices().at(-1)).toMatchObject({ type: 'success', title: 'Pipeline Started' }))
  })

  // The linked connection was deleted on the server (plans/stable-connections.md):
  // the page asks before making a new one, because a new one has a new webhook URL.
  describe('when the linked connection is gone', () => {
    const NEW = 'beef0000-1111-2222-3333-444444444444'

    // The 404 can come from the stop or from the update; both mean "gone".
    const gone: Array<[string, () => void]> = [
      ['stop', () => post.mockRejectedValueOnce(notFound())],
      ['update', () => { post.mockResolvedValueOnce({ data: {} } as never); put.mockRejectedValueOnce(notFound()) }],
    ]

    it.each(gone)('asks first when the %s says 404, and changes nothing until answered', async (_where, fail) => {
      seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
      fail()
      showBuilder()

      fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))

      expect(await screen.findByText('Pipeline no longer exists on the server')).toBeInTheDocument()
      expect(screen.getByText(/was deleted\. Deploy it as a new pipeline\?/)).toBeInTheDocument()
      expect(screen.getByText(new RegExp(`\\(${CONN.slice(0, 8)}…\\)`))).toBeInTheDocument()
      expect(screen.getByText(/Its webhook URL will change/)).toBeInTheDocument()
      // Nothing was created or started, and no success or error was reported.
      expect(calls(post)).not.toContain('/api/v1/connections')
      expect(calls(post).some((u) => u.endsWith('/start'))).toBe(false)
      expect(notices()).toHaveLength(0)
    })

    it('creates a new connection, not an update of the old id, and links the pipeline to it', async () => {
      seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
      post
        .mockRejectedValueOnce(notFound()) // stop CONN
        .mockResolvedValueOnce({ data: { data: { id: NEW } } } as never) // create
        .mockResolvedValueOnce({ data: {} } as never) // start NEW
      showBuilder()

      fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Deploy as new' }))

      await waitFor(() => expect(notices()).toHaveLength(1))
      expect(calls(post)).toEqual([`/api/v1/connections/${CONN}/stop`, '/api/v1/connections', `/api/v1/connections/${NEW}/start`])
      // The old id is never updated, and the create carries the pipeline.
      expect(put).not.toHaveBeenCalled()
      const body = post.mock.calls[1][1] as { name: string; nodes: Array<{ id: string }> }
      expect(body.name).toBe('Orders')
      expect(body.nodes.map((n) => n.id)).toEqual(['in-1', 'out-1'])

      expect(notices()[0]).toMatchObject({ type: 'success', title: 'Pipeline Started' })
      expect(notices()[0].message).toContain(`/webhook/${NEW}`)
      expect(notices()[0].message).not.toContain(CONN)
      // The canvas now points at the new connection and the dialog is closed.
      expect(useCanvasStore.getState().canvases[0].deployedConnectionId).toBe(NEW)
      expect(useUIStore.getState().confirmDialog).toBeNull()
      expect(screen.queryByText('Pipeline no longer exists on the server')).not.toBeInTheDocument()
    })

    it('creates and links the new connection when the update was what said 404', async () => {
      seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
      post
        .mockResolvedValueOnce({ data: {} } as never) // stop CONN
        .mockResolvedValueOnce({ data: { data: { id: NEW } } } as never) // create
        .mockResolvedValueOnce({ data: {} } as never) // start NEW
      put.mockRejectedValueOnce(notFound())
      showBuilder()

      fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Deploy as new' }))

      await waitFor(() => expect(notices()).toHaveLength(1))
      expect(calls(post)).toEqual([`/api/v1/connections/${CONN}/stop`, '/api/v1/connections', `/api/v1/connections/${NEW}/start`])
      expect(put).toHaveBeenCalledTimes(1)
      expect(put.mock.calls[0][0]).toBe(`/api/v1/connections/${CONN}`)
      expect(useCanvasStore.getState().canvases[0].deployedConnectionId).toBe(NEW)
    })

    it('does nothing more when the user cancels', async () => {
      seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
      post.mockRejectedValueOnce(notFound())
      showBuilder()

      fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))

      await waitFor(() => expect(useUIStore.getState().confirmDialog).toBeNull())
      expect(screen.queryByText('Pipeline no longer exists on the server')).not.toBeInTheDocument()
      // Only the stop that found the 404: no create, no update, no start, no toast.
      expect(calls(post)).toEqual([`/api/v1/connections/${CONN}/stop`])
      expect(put).not.toHaveBeenCalled()
      expect(notices()).toHaveLength(0)
      // Cancel keeps the link to the old id, so the next Deploy asks again (#671).
      expect(useCanvasStore.getState().canvases[0].deployedConnectionId).toBe(CONN)
      expect(useCanvasStore.getState().canvases[0].deployedName).toBe('Orders')
      expect(screen.queryByText(/DEPLOYED/)).not.toBeInTheDocument()
    })

    it('asks again on the next Deploy after Cancel, and still creates nothing', async () => {
      seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
      post.mockRejectedValue(notFound())
      showBuilder()

      fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
      await waitFor(() => expect(useUIStore.getState().confirmDialog).toBeNull())

      fireEvent.click(screen.getByRole('button', { name: 'Deploy' }))

      expect(await screen.findByText('Pipeline no longer exists on the server')).toBeInTheDocument()
      // Both clicks went to the old id; neither made a connection or started one.
      expect(calls(post)).toEqual([`/api/v1/connections/${CONN}/stop`, `/api/v1/connections/${CONN}/stop`])
      expect(put).not.toHaveBeenCalled()
      expect(notices()).toHaveLength(0)
      expect(useCanvasStore.getState().canvases[0].deployedConnectionId).toBe(CONN)
    })

    it('keeps the old link when creating the new connection fails, so the next Deploy asks again', async () => {
      seed(canvas({ deployedConnectionId: CONN, deployedName: 'Orders' }, true))
      post
        .mockRejectedValueOnce(notFound()) // stop CONN
        .mockRejectedValueOnce(new Error('integration count quota reached')) // create
      showBuilder()

      fireEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Deploy as new' }))

      await waitFor(() => expect(notices()).toHaveLength(1))
      expect(notices()[0]).toMatchObject({ type: 'error', title: 'Deployment failed' })
      expect(useCanvasStore.getState().canvases[0].deployedConnectionId).toBe(CONN)
    })
  })
})
