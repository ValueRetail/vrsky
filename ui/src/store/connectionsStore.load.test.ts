/**
 * #450: the connections store loads the current workspace's connections for
 * the header badge and the sidebar. Only the HTTP client is mocked.
 *
 * GET /api/v1/connections answers { data, total, limit, offset }
 * (src/pkg/managementapi/handler.go, ListResponse).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useConnectionsStore } from './connectionsStore'
import type { Connection } from '@/types/models'

const get = vi.mocked(apiClient.get)

const wireConnection = (id: string, name: string, status: string) => ({
  id,
  tenant_id: 'tenant-1',
  name,
  status,
  nodes: [],
  edges: [],
  created_at: '2026-10-20T09:22:54Z',
  updated_at: '2026-10-28T13:26:01Z',
})

const wireList = (items: ReturnType<typeof wireConnection>[], total: number) => ({
  data: { data: items, total, limit: 100, offset: 0 },
})

// A promise the test settles by hand, to order overlapping requests.
const deferred = <T>() => {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const state = () => useConnectionsStore.getState()

beforeEach(() => {
  get.mockReset()
  useConnectionsStore.setState(useConnectionsStore.getInitialState(), true)
})

describe('connectionsStore.load', () => {
  it('fetches the list and keeps its items and the API total', async () => {
    get.mockResolvedValue(
      wireList([wireConnection('conn-1', 'HTTP→BusinessCentral', 'running'), wireConnection('conn-2', 'Pipeline 2:26:01 PM', 'running')], 7) as never,
    )

    const pending = state().load()
    expect(state().loading).toBe(true)
    await pending

    expect(get).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/connections\?/))
    expect(state().connections.map((c) => c.id)).toEqual(['conn-1', 'conn-2'])
    expect(state().connections.map((c) => c.name)).toEqual(['HTTP→BusinessCentral', 'Pipeline 2:26:01 PM'])
    expect(state().total).toBe(7)
    expect(state().loading).toBe(false)
    expect(state().error).toBeNull()
  })

  it('a slow answer for an earlier load never overwrites the latest one', async () => {
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    get.mockReturnValueOnce(first.promise as never).mockReturnValueOnce(second.promise as never)

    const a = state().load() // workspace A
    const b = state().load() // switched to workspace B

    second.resolve(wireList([wireConnection('b-1', 'B one', 'stopped')], 1))
    await b
    expect(state().connections.map((c) => c.id)).toEqual(['b-1'])
    expect(state().total).toBe(1)
    expect(state().loading).toBe(false)

    first.resolve(wireList([wireConnection('a-1', 'A one', 'running'), wireConnection('a-2', 'A two', 'running')], 2))
    await a
    expect(state().connections.map((c) => c.id)).toEqual(['b-1'])
    expect(state().total).toBe(1)
    expect(state().loading).toBe(false)
  })

  it('stays loading until the latest load settles, even when an earlier one finishes first', async () => {
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    get.mockReturnValueOnce(first.promise as never).mockReturnValueOnce(second.promise as never)

    const a = state().load()
    const b = state().load()

    first.resolve(wireList([wireConnection('a-1', 'A one', 'running')], 1))
    await a
    expect(state().loading).toBe(true)

    second.resolve(wireList([wireConnection('b-1', 'B one', 'running'), wireConnection('b-2', 'B two', 'error')], 2))
    await b
    expect(state().loading).toBe(false)
    expect(state().connections.map((c) => c.id)).toEqual(['b-1', 'b-2'])
    expect(state().total).toBe(2)
  })

  it('a failed load sets error and drops the previous workspace\'s list and count', async () => {
    get.mockResolvedValueOnce(
      wireList([wireConnection('conn-1', 'a', 'running'), wireConnection('conn-2', 'b', 'running')], 2) as never,
    )
    await state().load()
    expect(state().total).toBe(2)

    get.mockRejectedValueOnce(new Error('HTTP Error 500'))
    await state().load()

    expect(state().error).toBeTruthy()
    expect(state().connections).toEqual([])
    expect(state().total).toBe(0)
    expect(state().loading).toBe(false)
  })
})

describe('connectionsStore.clear', () => {
  const conn = (id: string) => ({ id, name: id, status: 'running' }) as unknown as Connection

  it('clear resets the connections and the total', () => {
    useConnectionsStore.setState({ connections: [conn('x')], total: 7 })
    state().clear()
    expect(state().total).toBe(0)
    expect(state().connections).toEqual([])
  })
})

describe('connectionsStore.clear during a load', () => {
  it('a load still in flight when the store is cleared never writes its answer', async () => {
    const pending = deferred<unknown>()
    get.mockReturnValueOnce(pending.promise as never)

    const load = state().load()
    expect(state().loading).toBe(true)
    state().clear()
    expect(state().loading).toBe(false)

    pending.resolve(wireList([wireConnection('old-1', 'Previous user', 'running'), wireConnection('old-2', 'x', 'running')], 5))
    await load

    expect(state().connections).toEqual([])
    expect(state().total).toBe(0)
    expect(state().loading).toBe(false)
    expect(state().error).toBeNull()
  })

  it('a load that fails after the clear leaves no error behind', async () => {
    const pending = deferred<unknown>()
    get.mockReturnValueOnce(pending.promise as never)

    const load = state().load()
    state().clear()
    pending.reject(new Error('HTTP Error 500'))
    await load

    expect(state().error).toBeNull()
    expect(state().loading).toBe(false)
    expect(state().total).toBe(0)
  })
})
