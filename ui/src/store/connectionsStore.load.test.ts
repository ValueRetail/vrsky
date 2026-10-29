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

/**
 * #541: the sidebar's status cards must cover the whole workspace, not just
 * the loaded summary page. The mock below answers like the real handler
 * (ListConnections in src/pkg/managementapi/handler.go): limit 1..100 (else
 * 20), offset, and an optional status filter that the total counts too
 * (postgres_repository.go applies it to the COUNT query).
 */
describe('connectionsStore.load status totals (#541)', () => {
  type Wire = ReturnType<typeof wireConnection>

  // A workspace in the order the API pages it: all running first, so the
  // first 100 can be all running while the workspace is not.
  const workspace = (tag: string, running: number, stopped: number, error: number): Wire[] => [
    ...Array.from({ length: running }, (_, i) => wireConnection(`${tag}-r${i}`, `${tag} run ${i}`, 'running')),
    ...Array.from({ length: stopped }, (_, i) => wireConnection(`${tag}-s${i}`, `${tag} stop ${i}`, 'stopped')),
    ...Array.from({ length: error }, (_, i) => wireConnection(`${tag}-e${i}`, `${tag} err ${i}`, 'error')),
  ]

  const query = (url: string) => {
    const u = new URL(url, 'http://api.test')
    expect(u.pathname).toBe('/api/v1/connections')
    return u.searchParams
  }

  const respond = (items: Wire[], url: string) => {
    const q = query(url)
    const l = Number(q.get('limit'))
    const limit = Number.isInteger(l) && l > 0 && l <= 100 ? l : 20
    const o = Number(q.get('offset'))
    const offset = Number.isInteger(o) && o >= 0 ? o : 0
    const status = q.get('status') ?? ''
    const matches = status ? items.filter((c) => c.status === status) : items
    return { data: { data: matches.slice(offset, offset + limit), total: matches.length, limit, offset } }
  }

  // The fake server. Each request is answered from the workspace that was
  // current when it was sent; `hold` keeps matching requests until released,
  // `fail` makes matching requests answer 500.
  const fakeApi = () => {
    let items: Wire[] = []
    let hold: (q: URLSearchParams) => boolean = () => false
    let fail: (q: URLSearchParams) => boolean = () => false
    const held: { url: string; release: () => void }[] = []
    get.mockImplementation(((url: string) => {
      const answer = respond(items, url)
      const failed = fail(query(url))
      const settle = () => (failed ? Promise.reject(new Error('HTTP Error 500')) : Promise.resolve(answer))
      if (!hold(query(url))) return settle()
      return new Promise((resolve, reject) => {
        held.push({ url, release: () => void settle().then(resolve, reject) })
      })
    }) as never)
    return {
      use: (ws: Wire[]) => {
        items = ws
      },
      holdWhen: (fn: (q: URLSearchParams) => boolean) => {
        hold = fn
      },
      failWhen: (fn: (q: URLSearchParams) => boolean) => {
        fail = fn
      },
      held,
      releaseHeld: () => held.splice(0).forEach((h) => h.release()),
    }
  }

  const requests = () => get.mock.calls.map(([url]) => Object.fromEntries(query(url as string)))
  const statusRequests = () => requests().filter((r) => r.status !== undefined)
  const counts = () => state().statusCounts

  describe('criterion 1: a full page with more behind it fetches each status total', () => {
    it('250 connections, first 100 all running: totals come from the API (120 / 80 / 50)', async () => {
      const api = fakeApi()
      api.use(workspace('w', 120, 80, 50))

      await state().load()

      expect(state().connections).toHaveLength(100)
      expect(state().connections.every((c) => c.status === 'running')).toBe(true)
      expect(state().total).toBe(250)
      expect(counts()).toEqual({ running: 120, stopped: 80, error: 50 })
      expect(state().error).toBeNull()
      expect(state().loading).toBe(false)
    })

    it('asks for running, stopped and error totals, each with limit=1', async () => {
      const api = fakeApi()
      api.use(workspace('w', 120, 80, 50))

      await state().load()

      expect(statusRequests().map((r) => r.status).sort()).toEqual(['error', 'running', 'stopped'])
      for (const r of statusRequests()) expect(r.limit).toBe('1')
      expect(get).toHaveBeenCalledTimes(4)
    })

    it('counts statuses missing from the loaded page and keeps an empty status at 0', async () => {
      const api = fakeApi()
      api.use(workspace('w', 101, 0, 7))

      await state().load()

      expect(counts()).toEqual({ running: 101, stopped: 0, error: 7 })
    })

    it('while the status totals are out, the previous list, total and counts stay together', async () => {
      const api = fakeApi()
      api.use(workspace('small', 2, 1, 0))
      await state().load()

      api.use(workspace('big', 120, 80, 50))
      api.holdWhen((q) => q.has('status'))
      const pending = state().load()
      await vi.waitFor(() => expect(api.held).toHaveLength(3))

      // Nothing of the new answer is shown until its counts are in, so the
      // badge and the cards never describe two different loads.
      expect(state().connections.map((c) => c.id).sort()).toEqual(['small-r0', 'small-r1', 'small-s0'])
      expect(state().total).toBe(3)
      expect(counts()).toBeNull()

      api.releaseHeld()
      await pending
      expect(state().total).toBe(250)
      expect(counts()).toEqual({ running: 120, stopped: 80, error: 50 })
    })

    it('a later load of a smaller workspace drops the earlier totals', async () => {
      const api = fakeApi()
      api.use(workspace('big', 120, 80, 50))
      await state().load()
      expect(counts()).not.toBeNull()

      api.use(workspace('small', 2, 1, 0))
      await state().load()

      expect(state().total).toBe(3)
      expect(counts()).toBeNull()
    })
  })

  describe('criterion 2: no extra request when the page is the whole workspace', () => {
    it.each([
      ['an empty workspace', 0, 0, 0],
      ['a few connections', 2, 1, 3],
      ['99 connections', 50, 40, 9],
      ['exactly 100 with total 100', 60, 30, 10],
    ])('%s: one list request, nothing else', async (_name, running, stopped, error) => {
      const api = fakeApi()
      api.use(workspace('w', running, stopped, error))

      await state().load()

      expect(get).toHaveBeenCalledTimes(1)
      expect(statusRequests()).toEqual([])
      expect(requests()[0].limit).toBe('100')
      expect(state().connections).toHaveLength(running + stopped + error)
      expect(state().total).toBe(running + stopped + error)
      expect(state().error).toBeNull()
    })
  })

  describe('criterion 3: a stale load never writes its status totals', () => {
    it('status totals answering after a newer load (smaller workspace) write nothing', async () => {
      const api = fakeApi()
      api.use(workspace('a', 120, 80, 50))
      api.holdWhen((q) => q.has('status'))
      const a = state().load()
      await vi.waitFor(() => expect(api.held).toHaveLength(3))

      api.holdWhen(() => false)
      api.use(workspace('b', 1, 1, 1))
      await state().load()
      expect(state().total).toBe(3)

      api.releaseHeld()
      await a

      expect(state().connections.map((c) => c.id).sort()).toEqual(['b-e0', 'b-r0', 'b-s0'])
      expect(state().total).toBe(3)
      expect(counts()).toBeNull()
      expect(state().error).toBeNull()
      expect(state().loading).toBe(false)
    })

    it('status totals answering after a newer load (also large) keep the newer totals', async () => {
      const api = fakeApi()
      api.use(workspace('a', 120, 80, 50))
      api.holdWhen((q) => q.has('status'))
      const a = state().load()
      await vi.waitFor(() => expect(api.held).toHaveLength(3))

      api.holdWhen(() => false)
      api.use(workspace('b', 150, 5, 15))
      await state().load()
      expect(counts()).toEqual({ running: 150, stopped: 5, error: 15 })

      api.releaseHeld()
      await a

      expect(state().total).toBe(170)
      expect(counts()).toEqual({ running: 150, stopped: 5, error: 15 })
      expect(state().connections[0].id).toBe('b-r0')
    })

    it('a list answering after a newer load writes neither its list nor its totals', async () => {
      const api = fakeApi()
      api.use(workspace('a', 120, 80, 50))
      api.holdWhen((q) => !q.has('status'))
      const a = state().load()
      expect(api.held).toHaveLength(1)

      api.holdWhen(() => false)
      api.use(workspace('b', 0, 2, 0))
      await state().load()

      api.releaseHeld()
      await a

      expect(state().connections.map((c) => c.id)).toEqual(['b-s0', 'b-s1'])
      expect(state().total).toBe(2)
      expect(counts()).toBeNull()
      expect(state().loading).toBe(false)
    })

    it('status totals answering after clear() write nothing', async () => {
      const api = fakeApi()
      api.use(workspace('a', 120, 80, 50))
      api.holdWhen((q) => q.has('status'))
      const a = state().load()
      await vi.waitFor(() => expect(api.held).toHaveLength(3))

      state().clear()
      api.releaseHeld()
      await a

      expect(state().connections).toEqual([])
      expect(state().total).toBe(0)
      expect(counts()).toBeNull()
      expect(state().error).toBeNull()
      expect(state().loading).toBe(false)
    })

    it('a status request failing after a newer load leaves no error behind', async () => {
      const api = fakeApi()
      api.use(workspace('a', 120, 80, 50))
      api.holdWhen((q) => q.has('status'))
      api.failWhen((q) => q.get('status') === 'error')
      const a = state().load()
      await vi.waitFor(() => expect(api.held).toHaveLength(3))

      api.holdWhen(() => false)
      api.failWhen(() => false)
      api.use(workspace('b', 1, 0, 0))
      await state().load()

      api.releaseHeld()
      await a

      expect(state().error).toBeNull()
      expect(state().connections.map((c) => c.id)).toEqual(['b-r0'])
      expect(state().total).toBe(1)
    })
  })

  describe('criterion 4: a failed status request fails the load', () => {
    it.each(['running', 'stopped', 'error'])('status=%s answering 500 sets error and clears list, total and counts', async (failing) => {
      const api = fakeApi()
      api.use(workspace('w', 120, 80, 50))
      await state().load()
      expect(counts()).toEqual({ running: 120, stopped: 80, error: 50 })

      api.use(workspace('w2', 130, 70, 50))
      api.failWhen((q) => q.get('status') === failing)
      await state().load()

      expect(state().error).toBeTruthy()
      expect(state().connections).toEqual([])
      expect(state().total).toBe(0)
      expect(counts()).toBeNull()
      expect(state().loading).toBe(false)
    })
  })

  describe('criterion 5: clear and a failed load reset the status totals', () => {
    it('starts with no status totals', () => {
      expect(counts()).toBeNull()
    })

    it('clear() after a large load resets the totals', async () => {
      const api = fakeApi()
      api.use(workspace('w', 120, 80, 50))
      await state().load()
      expect(counts()).not.toBeNull()

      state().clear()

      expect(counts()).toBeNull()
      expect(state().total).toBe(0)
      expect(state().connections).toEqual([])
    })

    it('a failed list after a large load resets the totals', async () => {
      const api = fakeApi()
      api.use(workspace('w', 120, 80, 50))
      await state().load()
      expect(counts()).not.toBeNull()

      api.failWhen(() => true)
      await state().load()

      expect(state().error).toBeTruthy()
      expect(counts()).toBeNull()
      expect(state().total).toBe(0)
    })
  })
})
