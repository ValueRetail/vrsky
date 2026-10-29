/**
 * The builder's edit flow, /connections/:id/edit (#128, #502).
 *
 * #502: for a connection whose canvas was already linked, the flow only
 * switched to that canvas, so a name changed elsewhere left the tab and
 * deployedName both stale, and Deploy sent no name. It now fetches the
 * connection and syncs the name, keeping the canvas's own graph.
 *
 * #516: the route keeps the builder mounted when only :id changes, and the
 * flow ran once per mount, so A then B stayed on A's canvas. It now runs once
 * per connection id, and the name sync stays with the connection it was
 * found for.
 *
 * Runs the hook against the real canvas and UI stores; only the HTTP client
 * is mocked.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
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

    // Same builder, now routed to another connection, with conn-1's linked
    // canvas fresh in the hook's state. conn-2 has no canvas of its own, so
    // it is imported (#516); conn-1's canvas must not take its name.
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

// The route /connections/:id/edit keeps the builder mounted when only :id
// changes, so these rerender the same hook with another id.
describe('routing an open builder from one connection to another (#516)', () => {
  const realSync = useCanvasStore.getState().syncDeployedName
  // The hook is still mounted here (cleanup unmounts it later), hence act.
  afterEach(() => { act(() => useCanvasStore.setState({ syncDeployedName: realSync })) })
  // Records every (canvas, connection, server name) the hook asks the store to sync.
  const spySync = () => {
    const spy = vi.fn(realSync)
    useCanvasStore.setState({ syncDeployedName: spy })
    return spy
  }

  // A canvas deployed from here for `connId` as `deployedName`, with its own
  // graph. The default canvas stays current.
  const seedLinked = (connId: string, deployedName: string) => {
    const c = store().importCanvas(`Edit: ${deployedName}`, localNodes, [{ id: 'local-e', source: 'local-in', target: 'local-out' }], connId, deployedName)!
    store().switchCanvas(defaultId)
    return c.id
  }
  // The connections fetched, in order.
  const fetched = () => connectionGets().map(([url]) => String(url).slice('/api/v1/connections/'.length))
  // Answers each GET for `id` with the next server name in turn; the last repeats.
  const names = (id: string, ...ns: string[]) => {
    let i = 0
    return () => Promise.resolve(wireConnection(id, ns[Math.min(i++, ns.length - 1)]))
  }
  const deferred = () => {
    let resolve: (v: unknown) => void = () => {}
    let reject: (e: unknown) => void = () => {}
    const promise = new Promise((res, rej) => { resolve = res; reject = rej })
    return { promise, resolve, reject }
  }
  const linkedTo = (connId: string) => store().canvases.find((c) => c.deployedConnectionId === connId)

  it("A then B, B linked: opens B's canvas and syncs B's name onto it, keeping its graph", async () => {
    const a = seedLinked('conn-a', 'A')
    const b = seedLinked('conn-b', 'B old')
    routeGets({ 'conn-a': names('conn-a', 'A'), 'conn-b': names('conn-b', 'B new') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()

    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(canvas(b).deployedName).toBe('B new'))
    await settle()

    expect(store().currentCanvasId).toBe(b)
    expect(canvas(b)).toMatchObject({ name: 'Edit: B new', deployedName: 'B new', deployedConnectionId: 'conn-b' })
    expect(canvas(b).nodes).toEqual(localNodes)
    expect(canvas(a)).toMatchObject({ name: 'Edit: A', deployedName: 'A', deployedConnectionId: 'conn-a' })
    expect(canvas(a).nodes).toEqual(localNodes)
    expect(store().canvases).toHaveLength(3)
    expect(fetched()).toEqual(['conn-a', 'conn-b'])
    expect(notifications()).toEqual([])
  })

  it('A then B, B not linked: imports B as "Edit: <name>", linked to B, and opens it', async () => {
    const a = seedLinked('conn-a', 'A')
    routeGets({ 'conn-a': names('conn-a', 'A'), 'conn-b': names('conn-b', 'Orders to ERP') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()

    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(linkedTo('conn-b')).toBeDefined())
    await settle()

    const b = linkedTo('conn-b')!
    expect(b).toMatchObject({ name: 'Edit: Orders to ERP', deployedName: 'Orders to ERP' })
    expect(b.nodes.map((n) => n.type)).toEqual(['input', 'output'])
    expect(store().currentCanvasId).toBe(b.id)
    expect(canvas(a)).toMatchObject({ name: 'Edit: A', deployedName: 'A', deployedConnectionId: 'conn-a' })
    expect(store().canvases).toHaveLength(3)
    // One GET for B, for the import; the canvas it just imported is not
    // fetched again (#502).
    expect(fetched()).toEqual(['conn-a', 'conn-b'])
    expect(notifications()).toEqual([])
  })

  it.each([
    ['linked', true],
    ['not linked', false],
  ])("A's canvas is never synced against B (B %s)", async (_, bLinked) => {
    const a = seedLinked('conn-a', 'A')
    if (bLinked) seedLinked('conn-b', 'B')
    const sync = spySync()
    routeGets({ 'conn-a': names('conn-a', 'A'), 'conn-b': names('conn-b', 'B renamed') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(fetched()).toEqual(['conn-a']))
    await settle()
    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(fetched()).toContain('conn-b'))
    await settle()

    const b = linkedTo('conn-b')?.id
    // Every sync pairs a canvas with its own connection, and B's only when B
    // was linked before (an imported B already has the server's name).
    expect(sync.mock.calls).toEqual(bLinked ? [[a, 'conn-a', 'A'], [b, 'conn-b', 'B renamed']] : [[a, 'conn-a', 'A']])
    expect(canvas(a)).toMatchObject({ name: 'Edit: A', deployedName: 'A', deployedConnectionId: 'conn-a' })
    expect(fetched()).toEqual(['conn-a', 'conn-b'])
    expect(b).toBeDefined()
    expect(store().currentCanvasId).toBe(b)
  })

  it.each([
    ['answer', true],
    ['error', false],
  ])("routed to B while A's import fetch is out: A's late %s imports and shows nothing", async (_, answers) => {
    const lateA = deferred()
    routeGets({ 'conn-a': () => lateA.promise, 'conn-b': names('conn-b', 'B') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(fetched()).toEqual(['conn-a']))
    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(linkedTo('conn-b')).toBeDefined())
    await settle()
    const b = linkedTo('conn-b')!.id
    expect(store().currentCanvasId).toBe(b)

    // The user moves to another tab before A's answer lands; nothing may
    // pull them back to B or start B's load again.
    act(() => store().switchCanvas(defaultId))
    await act(async () => {
      if (answers) lateA.resolve(wireConnection('conn-a', 'A'))
      else lateA.reject(httpErr(500))
    })
    await settle()

    expect(linkedTo('conn-a')).toBeUndefined()
    expect(store().canvases.map((c) => c.id)).toEqual([defaultId, b])
    expect(canvas(b)).toMatchObject({ name: 'Edit: B', deployedName: 'B', deployedConnectionId: 'conn-b' })
    expect(store().currentCanvasId).toBe(defaultId)
    expect(fetched()).toEqual(['conn-a', 'conn-b'])
    expect(notifications()).toEqual([])
  })

  it("routed to B while A's name sync is out: A's late answer syncs nothing", async () => {
    const a = seedLinked('conn-a', 'A')
    const b = seedLinked('conn-b', 'B')
    const lateA = deferred()
    routeGets({ 'conn-a': () => lateA.promise, 'conn-b': names('conn-b', 'B') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(fetched()).toEqual(['conn-a']))
    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(store().currentCanvasId).toBe(b))
    await settle()
    await act(async () => { lateA.resolve(wireConnection('conn-a', 'A renamed')) })
    await settle()

    expect(canvas(a)).toMatchObject({ name: 'Edit: A', deployedName: 'A', deployedConnectionId: 'conn-a' })
    expect(store().currentCanvasId).toBe(b)
    expect(store().canvases).toHaveLength(3)
    expect(fetched()).toEqual(['conn-a', 'conn-b'])
  })

  it("A, B, back to A: returns to A's canvas with a fresh name sync, importing nothing", async () => {
    const a = seedLinked('conn-a', 'A')
    const b = seedLinked('conn-b', 'B')
    // A is renamed elsewhere while the builder is on B.
    routeGets({ 'conn-a': names('conn-a', 'A', 'A renamed'), 'conn-b': names('conn-b', 'B') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()
    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(store().currentCanvasId).toBe(b))
    await settle()
    view.rerender({ id: 'conn-a' })
    await waitFor(() => expect(canvas(a).deployedName).toBe('A renamed'))
    await settle()

    expect(store().currentCanvasId).toBe(a)
    expect(canvas(a)).toMatchObject({ name: 'Edit: A renamed', deployedConnectionId: 'conn-a' })
    expect(canvas(a).nodes).toEqual(localNodes)
    expect(store().canvases).toHaveLength(3)
    expect(fetched()).toEqual(['conn-a', 'conn-b', 'conn-a'])
    expect(notifications()).toEqual([])
  })

  it('a canvas imported here is not fetched again right after, but a later visit syncs it', async () => {
    const a = seedLinked('conn-a', 'A')
    routeGets({ 'conn-a': names('conn-a', 'A'), 'conn-b': names('conn-b', 'B', 'B renamed') })

    const view = renderEdit('conn-b')
    await waitFor(() => expect(linkedTo('conn-b')).toBeDefined())
    await settle()
    const b = linkedTo('conn-b')!.id
    expect(store().currentCanvasId).toBe(b)
    expect(fetched()).toEqual(['conn-b'])

    view.rerender({ id: 'conn-a' })
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()
    // B is renamed elsewhere while the builder is on A.
    view.rerender({ id: 'conn-b' })
    await waitFor(() => expect(canvas(b).deployedName).toBe('B renamed'))
    await settle()

    expect(store().currentCanvasId).toBe(b)
    expect(canvas(b)).toMatchObject({ name: 'Edit: B renamed', deployedConnectionId: 'conn-b' })
    expect(store().canvases.filter((c) => c.deployedConnectionId === 'conn-b')).toHaveLength(1)
    expect(store().canvases).toHaveLength(3)
    expect(fetched()).toEqual(['conn-b', 'conn-a', 'conn-b'])
    expect(notifications()).toEqual([])
  })

  it.each([
    ['linked', true],
    ['not linked', false],
  ])('the same id on a re-render loads only once (%s)', async (_, linked) => {
    if (linked) seedLinked('conn-a', 'A')
    routeGets({ 'conn-a': names('conn-a', 'A') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(linkedTo('conn-a')?.id))
    await settle()
    expect(fetched()).toEqual(['conn-a'])

    // The user moves to another tab and renames it (a new canvases array,
    // which the flow's effect depends on); the page re-renders with the same id.
    act(() => store().switchCanvas(defaultId))
    act(() => store().renameCanvas(defaultId, 'Scratch'))
    view.rerender({ id: 'conn-a' })
    view.rerender({ id: 'conn-a' })
    await settle()

    expect(fetched()).toEqual(['conn-a'])
    expect(store().canvases.filter((c) => c.deployedConnectionId === 'conn-a')).toHaveLength(1)
    expect(store().canvases).toHaveLength(2)
    expect(store().currentCanvasId).toBe(defaultId)
    expect(notifications()).toEqual([])
  })

  // The plain builder at / and the edit route share the mounted page, so
  // leaving for / and coming back is a rerender with undefined in between.
  it("A, the plain builder, back to A (linked): reopens A's canvas and syncs it again", async () => {
    const a = seedLinked('conn-a', 'A')
    // A is renamed elsewhere while the builder is on /.
    routeGets({ 'conn-a': names('conn-a', 'A', 'A renamed') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()
    view.rerender({ id: undefined })
    act(() => store().switchCanvas(defaultId))
    await settle()

    view.rerender({ id: 'conn-a' })
    await waitFor(() => expect(canvas(a).deployedName).toBe('A renamed'))
    await settle()

    expect(store().currentCanvasId).toBe(a)
    expect(canvas(a)).toMatchObject({ name: 'Edit: A renamed', deployedConnectionId: 'conn-a' })
    expect(canvas(a).nodes).toEqual(localNodes)
    expect(store().canvases).toHaveLength(2)
    expect(fetched()).toEqual(['conn-a', 'conn-a'])
    expect(notifications()).toEqual([])
  })

  it("A, the plain builder, back to A (imported here): reopens the imported canvas, syncs it, imports nothing more", async () => {
    routeGets({ 'conn-a': names('conn-a', 'A', 'A renamed') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(linkedTo('conn-a')).toBeDefined())
    await settle()
    const a = linkedTo('conn-a')!.id
    expect(fetched()).toEqual(['conn-a'])
    view.rerender({ id: undefined })
    act(() => store().switchCanvas(defaultId))
    await settle()

    view.rerender({ id: 'conn-a' })
    await waitFor(() => expect(canvas(a).deployedName).toBe('A renamed'))
    await settle()

    expect(store().currentCanvasId).toBe(a)
    expect(canvas(a)).toMatchObject({ name: 'Edit: A renamed', deployedConnectionId: 'conn-a' })
    expect(store().canvases.filter((c) => c.deployedConnectionId === 'conn-a')).toHaveLength(1)
    expect(store().canvases).toHaveLength(2)
    expect(fetched()).toEqual(['conn-a', 'conn-a'])
    expect(notifications()).toEqual([])
  })

  it('on the plain builder the flow makes no request and leaves the open tab alone', async () => {
    const a = seedLinked('conn-a', 'A')
    routeGets({ 'conn-a': names('conn-a', 'A') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()
    view.rerender({ id: undefined })
    act(() => store().switchCanvas(defaultId))
    act(() => store().renameCanvas(defaultId, 'Scratch'))
    view.rerender({ id: undefined })
    await settle()

    expect(fetched()).toEqual(['conn-a'])
    expect(store().currentCanvasId).toBe(defaultId)
    expect(store().canvases).toHaveLength(2)
    expect(canvas(a)).toMatchObject({ name: 'Edit: A', deployedName: 'A', deployedConnectionId: 'conn-a' })
  })

  it("A, the plain builder where A's canvas is closed, back to A: imports A again with one request", async () => {
    const a = seedLinked('conn-a', 'A')
    routeGets({ 'conn-a': names('conn-a', 'A') })

    const view = renderEdit('conn-a')
    await waitFor(() => expect(store().currentCanvasId).toBe(a))
    await settle()
    view.rerender({ id: undefined })
    act(() => store().deleteCanvas(a))
    await settle()

    view.rerender({ id: 'conn-a' })
    await waitFor(() => expect(linkedTo('conn-a')).toBeDefined())
    await settle()

    const reimported = linkedTo('conn-a')!
    expect(reimported.id).not.toBe(a)
    expect(reimported).toMatchObject({ name: 'Edit: A', deployedName: 'A' })
    expect(store().currentCanvasId).toBe(reimported.id)
    expect(store().canvases).toHaveLength(2)
    // One request for the import; none for the closed canvas's name.
    expect(fetched()).toEqual(['conn-a', 'conn-a'])
    expect(notifications()).toEqual([])
  })
})
