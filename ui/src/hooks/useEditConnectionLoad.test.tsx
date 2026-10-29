/**
 * The builder's edit flow, /connections/:id/edit (#128, #502).
 *
 * #502: for a connection whose canvas was already linked, the flow only
 * switched to that canvas, so a name changed elsewhere left the tab and
 * deployedName both stale, and Deploy sent no name. It now fetches the
 * connection and syncs the name, keeping the canvas's own graph.
 *
 * Runs the hook against the real canvas and UI stores; only the HTTP client
 * is mocked.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useCanvasStore } from '@/store/canvasStore'
import { useUIStore } from '@/store/uiStore'
import type { Node } from '@/types/pipeline'
import { useEditConnectionLoad } from './useEditConnectionLoad'

const get = vi.mocked(apiClient.get)

// The local graph of the linked canvas: positions the user arranged.
const localNodes: Node[] = [
  { id: 'local-in', type: 'input', position: { x: 40, y: 60 }, data: { label: 'Webhook', type: 'http' } },
  { id: 'local-out', type: 'output', position: { x: 400, y: 60 }, data: { label: 'BC', type: 'businesscentral' } },
]

// GET /api/v1/connections/:id answers { data: <connection> }.
const wireConnection = (id: string, name: string) => ({
  data: {
    data: {
      id,
      tenant_id: 'tenant-1',
      name,
      description: '',
      status: 'running',
      nodes: [
        { id: 'server-n1', type: 'consumer', config: { type: 'http' }, enabled: true },
        { id: 'server-n2', type: 'producer', config: { type: 'businesscentral' }, enabled: true },
      ],
      edges: [{ id: 'e1', source: 'server-n1', target: 'server-n2', order: 0 }],
      created_at: '2026-10-20T09:22:54Z',
      updated_at: '2026-10-29T08:00:00Z',
    },
  },
})

const connectionUrl = (id: string) => `/api/v1/connections/${id}`
const httpErr = (status: number) => Object.assign(new Error(`HTTP ${status}`), { response: { status } })

// Answers GET /api/v1/connections/:id from the table; anything else is a bug.
const routeGets = (connections: Record<string, () => Promise<unknown>>) =>
  get.mockImplementation(((url: string) => {
    const m = url.match(/^\/api\/v1\/connections\/([^/?]+)$/)
    if (m && connections[m[1]]) return connections[m[1]]()
    return Promise.reject(new Error(`unexpected GET ${url}`))
  }) as never)

const connectionGets = () => get.mock.calls.filter(([url]) => /^\/api\/v1\/connections\/[^/?]+$/.test(String(url)))

const store = () => useCanvasStore.getState()
const canvas = (id: string) => store().getCanvasById(id)!
const storedCanvas = (id: string) => {
  const raw = JSON.parse(localStorage.getItem('vrsky:canvases:v1:tenant-1') ?? '{}')
  return (raw.canvases ?? []).find((c: { id: string }) => c.id === id)
}
const notifications = () => useUIStore.getState().notifications
// Lets pending promise callbacks (the fetch answer) run.
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

const renderEdit = (id: string | undefined) =>
  renderHook(({ id }: { id: string | undefined }) => useEditConnectionLoad(id), { initialProps: { id } })

// A canvas deployed from here as "Old name", with its own graph, and not the
// current one, so the edit flow has to switch to it.
let linkedId: string
let defaultId: string
const seedLinkedCanvas = (tab = 'Edit: Old name', deployedName = 'Old name') => {
  const c = store().importCanvas(tab, localNodes, [{ id: 'local-e', source: 'local-in', target: 'local-out' }], 'conn-1', deployedName)!
  linkedId = c.id
  store().switchCanvas(defaultId)
}

beforeEach(() => {
  localStorage.clear()
  get.mockReset()
  useUIStore.setState({ notifications: [] })
  useCanvasStore.setState({ canvases: [], currentCanvasId: null, isInitialized: false, tenantId: null })
  store().initialize('tenant-1')
  defaultId = store().currentCanvasId!
})

describe('edit route on a canvas already linked to the connection (#502)', () => {
  it('switches to it and takes the server name, keeping the canvas graph', async () => {
    seedLinkedCanvas()
    routeGets({ 'conn-1': () => Promise.resolve(wireConnection('conn-1', 'New name')) })

    renderEdit('conn-1')

    await waitFor(() => expect(canvas(linkedId).deployedName).toBe('New name'))
    expect(get).toHaveBeenCalledWith(connectionUrl('conn-1'))
    expect(canvas(linkedId)).toMatchObject({ name: 'Edit: New name', deployedName: 'New name', deployedConnectionId: 'conn-1' })
    expect(storedCanvas(linkedId)).toMatchObject({ name: 'Edit: New name', deployedName: 'New name' })

    // The linked canvas is opened with its own nodes, not the API's graph,
    // and no second canvas is imported for the connection.
    expect(store().currentCanvasId).toBe(linkedId)
    expect(canvas(linkedId).nodes).toEqual(localNodes)
    expect(canvas(linkedId).edges).toEqual([{ id: 'local-e', source: 'local-in', target: 'local-out' }])
    await settle()
    expect(store().canvases).toHaveLength(2)
    expect(connectionGets()).toHaveLength(1)
    expect(notifications()).toEqual([])
  })

  it('a tab renamed here keeps its name, and deployedName takes the server name', async () => {
    seedLinkedCanvas('Mine', 'Old name')
    routeGets({ 'conn-1': () => Promise.resolve(wireConnection('conn-1', 'New name')) })

    renderEdit('conn-1')

    await waitFor(() => expect(canvas(linkedId).deployedName).toBe('New name'))
    expect(canvas(linkedId).name).toBe('Mine')
  })

  it('a failed fetch changes nothing and shows no error', async () => {
    seedLinkedCanvas()
    routeGets({ 'conn-1': () => Promise.reject(httpErr(500)) })

    renderEdit('conn-1')

    await waitFor(() => expect(get).toHaveBeenCalledWith(connectionUrl('conn-1')))
    await settle()
    expect(store().currentCanvasId).toBe(linkedId)
    expect(canvas(linkedId)).toMatchObject({ name: 'Edit: Old name', deployedName: 'Old name' })
    expect(canvas(linkedId).nodes).toEqual(localNodes)
    expect(notifications()).toEqual([])
  })

  it('an answer that lands after the builder is gone changes nothing', async () => {
    seedLinkedCanvas()
    let answer: (v: unknown) => void = () => {}
    routeGets({ 'conn-1': () => new Promise((resolve) => { answer = resolve }) })

    const view = renderEdit('conn-1')
    await waitFor(() => expect(get).toHaveBeenCalledWith(connectionUrl('conn-1')))
    view.unmount()
    await act(async () => { answer(wireConnection('conn-1', 'New name')) })
    await settle()

    expect(canvas(linkedId)).toMatchObject({ name: 'Edit: Old name', deployedName: 'Old name' })
  })

  it("another connection's name never lands on this canvas", async () => {
    seedLinkedCanvas()
    let answerOther: (v: unknown) => void = () => {}
    routeGets({
      'conn-1': () => Promise.resolve(wireConnection('conn-1', 'Old name')),
      'conn-2': () => new Promise((resolve) => { answerOther = resolve }),
    })

    const view = renderEdit('conn-1')
    await waitFor(() => expect(get).toHaveBeenCalledWith(connectionUrl('conn-1')))
    await settle()

    // Same builder, now routed to another connection: the linked canvas id
    // from conn-1 is still in the hook's state.
    view.rerender({ id: 'conn-2' })
    await waitFor(() => expect(get).toHaveBeenCalledWith(connectionUrl('conn-2')))
    await act(async () => { answerOther(wireConnection('conn-2', 'Other pipeline')) })
    await settle()

    expect(canvas(linkedId)).toMatchObject({ name: 'Edit: Old name', deployedName: 'Old name', deployedConnectionId: 'conn-1' })
  })
})

// The #128 paths, moved here unchanged.
describe('edit route with no canvas linked to the connection', () => {
  it('imports the connection as "Edit: <name>", linked, with its graph, and opens it', async () => {
    routeGets({ 'conn-1': () => Promise.resolve(wireConnection('conn-1', 'Orders to ERP')) })

    renderEdit('conn-1')

    await waitFor(() => expect(store().canvases).toHaveLength(2))
    const imported = store().canvases.find((c) => c.id !== defaultId)!
    expect(imported).toMatchObject({ name: 'Edit: Orders to ERP', deployedConnectionId: 'conn-1', deployedName: 'Orders to ERP' })
    expect(imported.nodes.map((n) => n.type)).toEqual(['input', 'output'])
    expect(imported.edges).toHaveLength(1)
    expect(store().currentCanvasId).toBe(imported.id)
    expect(storedCanvas(imported.id)).toMatchObject({ name: 'Edit: Orders to ERP', deployedConnectionId: 'conn-1' })
    await settle()
    // Imported once, and still as imported once everything has settled.
    expect(store().canvases).toHaveLength(2)
    expect(canvas(imported.id)).toMatchObject({ name: 'Edit: Orders to ERP', deployedName: 'Orders to ERP' })
    expect(store().currentCanvasId).toBe(imported.id)
    expect(notifications()).toEqual([])
    // The canvas it just imported already has the server's name; no second fetch.
    expect(get.mock.calls.filter(([url]) => url === connectionUrl('conn-1'))).toHaveLength(1)
  })

  it('a connection with no name is imported under the start of its id', async () => {
    routeGets({ 'conn-1234567890': () => Promise.resolve(wireConnection('conn-1234567890', '')) })

    renderEdit('conn-1234567890')

    await waitFor(() => expect(store().canvases).toHaveLength(2))
    expect(store().canvases.find((c) => c.id !== defaultId)).toMatchObject({ name: 'Edit: conn-123', deployedConnectionId: 'conn-1234567890' })
  })

  it('a failed fetch shows an error and imports nothing', async () => {
    routeGets({ 'conn-1': () => Promise.reject(httpErr(404)) })

    renderEdit('conn-1')

    await waitFor(() => expect(notifications()).toHaveLength(1))
    expect(notifications()[0]).toMatchObject({ type: 'error', title: 'Edit connection' })
    expect(store().canvases.map((c) => c.id)).toEqual([defaultId])
    expect(store().currentCanvasId).toBe(defaultId)
  })

  it('an answer that lands after the builder is gone imports nothing and shows nothing', async () => {
    let answer: (v: unknown) => void = () => {}
    routeGets({ 'conn-1': () => new Promise((resolve) => { answer = resolve }) })

    const view = renderEdit('conn-1')
    await waitFor(() => expect(get).toHaveBeenCalledWith(connectionUrl('conn-1')))
    view.unmount()
    await act(async () => { answer(wireConnection('conn-1', 'Orders to ERP')) })
    await settle()

    expect(store().canvases.map((c) => c.id)).toEqual([defaultId])
    expect(notifications()).toEqual([])
  })
})

describe('when the edit flow does nothing', () => {
  it('without a connection id it makes no request and changes nothing', async () => {
    renderEdit(undefined)
    await settle()
    expect(get).not.toHaveBeenCalled()
    expect(store().canvases.map((c) => c.id)).toEqual([defaultId])
  })

  it('waits for the canvas store to load before looking for a linked canvas', async () => {
    useCanvasStore.setState({ canvases: [], currentCanvasId: null, isInitialized: false, tenantId: null })
    routeGets({ 'conn-1': () => Promise.resolve(wireConnection('conn-1', 'Orders to ERP')) })

    renderEdit('conn-1')
    await settle()
    expect(get).not.toHaveBeenCalled()

    act(() => store().initialize('tenant-1'))

    await waitFor(() => expect(store().canvases.some((c) => c.name === 'Edit: Orders to ERP')).toBe(true))
    expect(get).toHaveBeenCalledWith(connectionUrl('conn-1'))
    await settle()
    expect(store().canvases.filter((c) => c.deployedConnectionId === 'conn-1')).toHaveLength(1)
  })
})
