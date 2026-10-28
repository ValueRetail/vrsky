/**
 * #450: rename the builder tab, then Deploy. Drives the real canvas store and
 * the real deployPlan + deployCanvas the way PipelineBuilder does, against a
 * fake HTTP client that records every call.
 *
 * A linked canvas updates in place (stop, PUT on its id); the PUT carries the
 * tab's name only when the tab was renamed, never a generated
 * `Pipeline <time>`, and never a description. "Deploy as new" (#355) still
 * creates, named after the tab.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useCanvasStore } from '@/store/canvasStore'
import { ConnectionGone, deployCanvas, deployPlan } from './deployPipeline'

type Call = { method: 'post' | 'put'; url: string; body: unknown }

let calls: Call[] = []
const post = vi.fn()
const put = vi.fn()
const client = { post, put } as never

const now = new Date('2026-10-28T13:26:01Z')

beforeEach(() => {
  localStorage.clear()
  useCanvasStore.setState({ canvases: [], currentCanvasId: null, isInitialized: false, tenantId: null })
  useCanvasStore.getState().initialize('tenant-1')
  calls = []
  post.mockReset()
  put.mockReset()
  post.mockImplementation((url: string, body?: unknown) => {
    calls.push({ method: 'post', url, body })
    if (url === '/api/v1/connections') return Promise.resolve({ data: { data: { id: 'new-1' } } })
    return Promise.resolve({ data: {} })
  })
  put.mockImplementation((url: string, body?: unknown) => {
    calls.push({ method: 'put', url, body })
    return Promise.resolve({ data: {} })
  })
})

const canvas = (id: string) => useCanvasStore.getState().getCanvasById(id)!

// One Deploy click, as PipelineBuilder.deployPipeline does it.
const deploy = async (canvasId: string, asNew = false) => {
  const active = canvas(canvasId)
  const plan = deployPlan(active, asNew, now)
  const payload = { ...plan.identity, nodes: [], edges: [] }
  const link = (id: string | null, name?: string) =>
    useCanvasStore.getState().setDeployedConnectionId(canvasId, id, name)
  return deployCanvas(client, payload, plan.previousConnectionId, link)
}

const putBodies = () => calls.filter((c) => c.method === 'put').map((c) => c.body as Record<string, unknown>)
const creates = () => calls.filter((c) => c.method === 'post' && c.url === '/api/v1/connections')

describe('rename tab, then Deploy, on a canvas linked to a connection', () => {
  it('sends the new name in the PUT on the linked id; a second deploy sends none', async () => {
    const c = useCanvasStore.getState().importCanvas('Edit: Pipeline 11:22:54 AM', [], [], 'conn-1', 'Pipeline 11:22:54 AM')!
    useCanvasStore.getState().renameCanvas(c.id, 'HTTP→BusinessCentral')
    expect(useCanvasStore.getState().getActiveCanvas()?.id).toBe(c.id)
    expect(canvas(c.id).name).toBe('HTTP→BusinessCentral')

    const result = await deploy(c.id)

    expect(result).toEqual({ connectionId: 'conn-1', created: false })
    expect(calls.map((x) => `${x.method} ${x.url}`)).toEqual([
      'post /api/v1/connections/conn-1/stop',
      'put /api/v1/connections/conn-1',
    ])
    expect(putBodies()[0].name).toBe('HTTP→BusinessCentral')
    expect(putBodies()[0]).not.toHaveProperty('description')
    expect(creates()).toHaveLength(0)
    expect(canvas(c.id).deployedConnectionId).toBe('conn-1')
    expect(canvas(c.id).deployedName).toBe('HTTP→BusinessCentral')

    calls = []
    await deploy(c.id)
    expect(calls.map((x) => `${x.method} ${x.url}`)).toEqual([
      'post /api/v1/connections/conn-1/stop',
      'put /api/v1/connections/conn-1',
    ])
    expect(putBodies()[0]).not.toHaveProperty('name')
    expect(creates()).toHaveLength(0)
  })

  it('a deploy without a rename sends no name, no description, never Pipeline <time>', async () => {
    const c = useCanvasStore.getState().importCanvas('Edit: Pipeline 11:22:54 AM', [], [], 'conn-1', 'Pipeline 11:22:54 AM')!

    await deploy(c.id)

    expect(putBodies()).toHaveLength(1)
    const body = putBodies()[0]
    expect(body).not.toHaveProperty('name')
    expect(body).not.toHaveProperty('description')
    expect(String(body.name ?? '')).not.toMatch(/^Pipeline /)
    expect(creates()).toHaveLength(0)
  })

  it('a canvas linked without a known server name: no name until renamed, then the new one', async () => {
    // The default canvas a fresh builder starts with.
    const c = useCanvasStore.getState().getActiveCanvas()!
    expect(c.name).toBe('Tenant 1')
    // Linked by an older version of the builder, which recorded no name.
    useCanvasStore.getState().setDeployedConnectionId(c.id, 'conn-2')
    expect(canvas(c.id).deployedName).toBeUndefined()

    await deploy(c.id)
    expect(calls.map((x) => `${x.method} ${x.url}`)).toEqual([
      'post /api/v1/connections/conn-2/stop',
      'put /api/v1/connections/conn-2',
    ])
    expect(putBodies()[0]).not.toHaveProperty('name')
    expect(putBodies()[0]).not.toHaveProperty('description')

    calls = []
    useCanvasStore.getState().renameCanvas(c.id, 'BusinessCentral→RemoteAgent')
    await deploy(c.id)
    expect(calls.map((x) => `${x.method} ${x.url}`)).toEqual([
      'post /api/v1/connections/conn-2/stop',
      'put /api/v1/connections/conn-2',
    ])
    expect(putBodies()[0].name).toBe('BusinessCentral→RemoteAgent')
    expect(creates()).toHaveLength(0)
    expect(canvas(c.id).deployedName).toBe('BusinessCentral→RemoteAgent')
  })
})

describe('Deploy as new after the linked connection is gone (#355)', () => {
  it('unlinks on 404, then creates once, named after the tab, and links the new id', async () => {
    const httpErr = (status: number) => Object.assign(new Error(`HTTP ${status}`), { response: { status } })
    post.mockImplementation((url: string, body?: unknown) => {
      calls.push({ method: 'post', url, body })
      if (url === '/api/v1/connections/gone-1/stop') return Promise.reject(httpErr(404))
      if (url === '/api/v1/connections') return Promise.resolve({ data: { data: { id: 'new-1' } } })
      return Promise.resolve({ data: {} })
    })
    put.mockImplementation((url: string, body?: unknown) => {
      calls.push({ method: 'put', url, body })
      return url === '/api/v1/connections/gone-1' ? Promise.reject(httpErr(404)) : Promise.resolve({ data: {} })
    })
    const c = useCanvasStore.getState().importCanvas('Edit: Orders to ERP', [], [], 'gone-1', 'Orders to ERP')!

    const err = await deploy(c.id).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ConnectionGone)
    expect((err as ConnectionGone).connectionId).toBe('gone-1')
    expect(canvas(c.id).deployedConnectionId).toBeUndefined()
    expect(canvas(c.id).deployedName).toBeUndefined()
    expect(creates()).toHaveLength(0)

    calls = []
    const result = await deploy(c.id, true)

    expect(result).toEqual({ connectionId: 'new-1', created: true })
    expect(creates()).toHaveLength(1)
    expect(calls.filter((x) => x.url.includes('gone-1'))).toHaveLength(0)
    const body = creates()[0].body as Record<string, unknown>
    expect(body.name).toBe('Orders to ERP')
    expect(canvas(c.id).deployedConnectionId).toBe('new-1')
    expect(canvas(c.id).deployedName).toBe('Orders to ERP')
  })
})
