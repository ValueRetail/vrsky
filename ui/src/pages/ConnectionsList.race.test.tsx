/**
 * #501: only the latest list load may write.
 *
 * The list reloads when the page or the workspace changes. On a slow switch
 * from workspace A to B, A's answer could land after B's and the list showed
 * A's connections under B. These tests hold every list request open and
 * answer them in the bad order. Only the HTTP client is mocked; the real
 * component, router and stores run.
 *
 * #515: a delete that answers after a reload leaves the reload's total
 * alone, and a failed load shows an error state with "Try again" instead of
 * "No connections yet". Same harness, deletes held open too.
 */

import { Profiler } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient, { VRSkyAPIError } from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { useConnectionsStore } from '@/store/connectionsStore'
import type { Tenant } from '@/types/models'
import ConnectionsList from './ConnectionsList'

const get = vi.mocked(apiClient.get)
const del = vi.mocked(apiClient.delete)

const tenant = (id: string, name: string): Tenant => ({
  id,
  name,
  slug: id,
  owner_id: 'user-1',
  subscription_plan: 'trial',
  is_verified: true,
  user_role: 'owner',
  max_integrations: 10,
  max_messages_per_month: 1000,
  status: 'active',
  billing_status: 'trial',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
})

const TENANT_A = tenant('tenant-a', 'Workspace A')
const TENANT_B = tenant('tenant-b', 'Workspace B')
const TENANT_C = tenant('tenant-c', 'Workspace C')

const wireConnection = (tenantId: string, id: string, name: string) => ({
  id,
  tenant_id: tenantId,
  name,
  description: '',
  status: 'running',
  nodes: [
    { id: 'n1', type: 'consumer', config: { type: 'http' }, enabled: true },
    { id: 'n2', type: 'producer', config: { type: 'businesscentral' }, enabled: true },
  ],
  edges: [{ id: 'e1', source: 'n1', target: 'n2', order: 0 }],
  created_at: '2026-10-20T09:22:54Z',
  updated_at: '2026-10-28T13:26:01Z',
})

const listBody = (tenantId: string, names: string[], total = names.length, offset = 0) => ({
  data: names.map((n, i) => wireConnection(tenantId, `${tenantId}-${offset + i}`, n)),
  total,
  limit: 10,
  offset,
})

const A_NAMES = ['A secret orders', 'A secret invoices']
const B_NAMES = ['B public feed']

interface Deferred {
  resolve: (value: unknown) => void
  reject: (reason: unknown) => void
}

// One entry per list request, in call order, with the workspace that was
// current when the request went out. The mocked client skips the axios
// interceptor that sets X-Tenant-ID, so the store's tenant at call time is
// what that header would have carried.
interface PendingCall extends Deferred {
  tenantId: string | undefined
  url: string
}

let calls: PendingCall[] = []

const holdEveryListRequest = () => {
  get.mockImplementation(((url: string) => {
    if (!/^\/api\/v1\/connections(\?|$)/.test(url)) return Promise.resolve({ data: {} })
    return new Promise((resolve, reject) => {
      calls.push({ tenantId: useAuthStore.getState().currentTenant?.id, url, resolve, reject })
    })
  }) as never)
}

const callsFor = (tenantId: string) => calls.filter((c) => c.tenantId === tenantId)
const onlyCallFor = (tenantId: string): PendingCall => {
  const matching = callsFor(tenantId)
  expect(matching).toHaveLength(1)
  return matching[0]
}

// Let pending promise chains (mocked client -> service -> component) run
// to the end inside act, so every state update they cause is applied.
const flush = () => act(async () => {
  for (let i = 0; i < 10; i++) await Promise.resolve()
})

// Settle a request and let React apply whatever the component does with it.
const answer = async (call: PendingCall, body: unknown) => {
  call.resolve({ data: body })
  await flush()
}
const fail = async (call: PendingCall, reason: unknown) => {
  call.reject(reason)
  await flush()
}

const switchTo = async (t: Tenant) => {
  await act(async () => {
    useAuthStore.setState({ currentTenant: t })
  })
}

const errorNotifications = () => useUIStore.getState().notifications.filter((n) => n.type === 'error')

// Every React commit of the list calls these, right after the DOM was
// updated. A MutationObserver only sees changes, so it would miss rows that
// a commit should have removed but left in place; this sees every commit.
const commitListeners = new Set<() => void>()
const onCommit = () => commitListeners.forEach((l) => l())

// Records whether any of the given texts was in the document at any commit
// or DOM change after watching started, not only at the moment of an
// assertion. Start it before the step under test.
const watchFor = (texts: string[]) => {
  const seen = new Set<string>()
  const check = () => {
    const content = document.body.textContent ?? ''
    for (const t of texts) if (content.includes(t)) seen.add(t)
  }
  const observer = new MutationObserver(check)
  observer.observe(document.body, { childList: true, subtree: true, characterData: true })
  commitListeners.add(check)
  return {
    seen: () => {
      // MutationObserver callbacks are microtasks; take a final look too.
      check()
      return [...seen]
    },
    stop: () => {
      observer.disconnect()
      commitListeners.delete(check)
    },
  }
}

const skeletonShown = () => document.querySelectorAll('.animate-pulse').length > 0

const showList = () =>
  render(
    <MemoryRouter>
      <Profiler id="connections-list" onRender={onCommit}>
        <ConnectionsList />
      </Profiler>
    </MemoryRouter>,
  )

beforeEach(() => {
  commitListeners.clear()
  calls = []
  get.mockReset()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useUIStore.setState(useUIStore.getInitialState(), true)
  useAuthStore.setState({ currentTenant: TENANT_A })
  holdEveryListRequest()
})

describe('ConnectionsList workspace switch race (#501)', () => {
  it("never shows A's connections under B when A answers after B", async () => {
    showList()
    const callA = onlyCallFor(TENANT_A.id)

    await switchTo(TENANT_B)
    const callB = onlyCallFor(TENANT_B.id)
    const watcher = watchFor(A_NAMES)

    await answer(callB, listBody(TENANT_B.id, B_NAMES))
    expect(await screen.findByText('B public feed')).toBeInTheDocument()

    // A's slow answer lands last.
    await answer(callA, listBody(TENANT_A.id, A_NAMES))

    expect(screen.getByText('B public feed')).toBeInTheDocument()
    for (const name of A_NAMES) expect(screen.queryByText(name)).not.toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    watcher.stop()
    expect(errorNotifications()).toEqual([])
  })

  it("never shows A's connections when A answers first but after the switch", async () => {
    // A answers while B is still loading. A's rows must not flash on screen
    // in between, and B's answer must still win.
    showList()
    const callA = onlyCallFor(TENANT_A.id)

    await switchTo(TENANT_B)
    const callB = onlyCallFor(TENANT_B.id)
    const watcher = watchFor(A_NAMES)

    await answer(callA, listBody(TENANT_A.id, A_NAMES))
    expect(watcher.seen()).toEqual([])

    await answer(callB, listBody(TENANT_B.id, B_NAMES))
    expect(await screen.findByText('B public feed')).toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    watcher.stop()
  })

  it("shows no error under B when A's late request fails", async () => {
    showList()
    const callA = onlyCallFor(TENANT_A.id)

    await switchTo(TENANT_B)
    const callB = onlyCallFor(TENANT_B.id)

    await answer(callB, listBody(TENANT_B.id, B_NAMES))
    expect(await screen.findByText('B public feed')).toBeInTheDocument()

    await fail(callA, new Error('workspace A timed out'))

    expect(errorNotifications()).toEqual([])
    expect(screen.getByText('B public feed')).toBeInTheDocument()
    expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
  })

  it("clears A's rows and reports the error when B's load fails", async () => {
    showList()
    await answer(onlyCallFor(TENANT_A.id), listBody(TENANT_A.id, A_NAMES))
    expect(await screen.findByText('A secret orders')).toBeInTheDocument()
    expect(errorNotifications()).toEqual([])

    await switchTo(TENANT_B)
    await fail(onlyCallFor(TENANT_B.id), new Error('network down'))

    for (const name of A_NAMES) expect(screen.queryByText(name)).not.toBeInTheDocument()
    expect(errorNotifications()).toHaveLength(1)
    expect(errorNotifications()[0].message).toBe('Failed to load connections')
    // The failed load is B's answer: the page settles on B's error state
    // instead of hanging on the skeleton, and doesn't call B empty (#515).
    expect(skeletonShown()).toBe(false)
    expect(screen.getByText("Couldn't load connections")).toBeInTheDocument()
    expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
  })

  it('keeps the newer page when an older page answers late in the same workspace', async () => {
    showList()
    const page1Names = Array.from({ length: 10 }, (_, i) => `Page one row ${i}`)
    const page2Names = Array.from({ length: 10 }, (_, i) => `Page two row ${i}`)

    await answer(calls[0], listBody(TENANT_A.id, page1Names, 25, 0))
    expect(await screen.findByText('Page 1 of 3')).toBeInTheDocument()

    // Go to page 2, then back to page 1 before page 2 answers.
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await flush()
    expect(calls).toHaveLength(2)
    expect(calls[1].url).toContain('offset=10')

    fireEvent.click(screen.getByRole('button', { name: 'Previous' }))
    await flush()
    expect(calls).toHaveLength(3)
    expect(calls[2].url).toContain('offset=0')

    const watcher = watchFor(page2Names)
    await answer(calls[2], listBody(TENANT_A.id, page1Names, 25, 0))
    expect(await screen.findByText('Page one row 0')).toBeInTheDocument()

    // The stale page-2 answer arrives last.
    await answer(calls[1], listBody(TENANT_A.id, page2Names, 25, 10))

    expect(screen.getByText('Page 1 of 3')).toBeInTheDocument()
    expect(screen.getByText('Page one row 0')).toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    watcher.stop()
  })
  it("drops A's rows in the very first render after the switch", async () => {
    showList()
    await answer(onlyCallFor(TENANT_A.id), listBody(TENANT_A.id, A_NAMES))
    expect(await screen.findByText('A secret orders')).toBeInTheDocument()

    // Watch from before the switch, so the commit that switches is checked
    // too, before any request for B has been answered.
    const watcher = watchFor(A_NAMES)
    const emptyWatcher = watchFor(['No connections yet'])
    await switchTo(TENANT_B)

    expect(watcher.seen()).toEqual([])
    // B hasn't answered, so the page must not claim B has no connections.
    expect(emptyWatcher.seen()).toEqual([])
    expect(skeletonShown()).toBe(true)

    await answer(onlyCallFor(TENANT_B.id), listBody(TENANT_B.id, B_NAMES))
    expect(await screen.findByText('B public feed')).toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    expect(emptyWatcher.seen()).toEqual([])
    watcher.stop()
    emptyWatcher.stop()
  })

  it("never shows A's rows when A's next-page answer lands while B loads", async () => {
    // Reviewer's sequence: A page 1 loaded, Next (A page 2 pending), switch
    // to B, then A's page-2 answer arrives before B's.
    showList()
    const aPage1 = Array.from({ length: 10 }, (_, i) => `A page one row ${i}`)
    const aPage2 = Array.from({ length: 10 }, (_, i) => `A page two row ${i}`)
    await answer(calls[0], listBody(TENANT_A.id, aPage1, 25, 0))
    expect(await screen.findByText('Page 1 of 3')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await flush()
    const aNext = calls[calls.length - 1]
    expect(aNext.tenantId).toBe(TENANT_A.id)
    expect(aNext.url).toContain('offset=10')

    const watcher = watchFor([...aPage1, ...aPage2])
    await switchTo(TENANT_B)
    const bCalls = callsFor(TENANT_B.id)
    expect(bCalls.length).toBeGreaterThan(0)
    // The page resets to 1 for the new workspace; the last request is B's page 1.
    const bLatest = bCalls[bCalls.length - 1]
    expect(bLatest.url).toContain('offset=0')

    // A's page count doesn't leak either while B loads.
    expect(screen.queryByText(/Page \d+ of/)).not.toBeInTheDocument()

    await answer(aNext, listBody(TENANT_A.id, aPage2, 25, 10))
    expect(watcher.seen()).toEqual([])
    expect(skeletonShown()).toBe(true)
    expect(screen.queryByText(/Page \d+ of/)).not.toBeInTheDocument()

    await answer(bLatest, listBody(TENANT_B.id, B_NAMES))
    expect(await screen.findByText('B public feed')).toBeInTheDocument()

    // Any older B request (the page-2 one before the reset) lands last.
    for (const stale of bCalls.slice(0, -1)) {
      await answer(stale, listBody(TENANT_B.id, ['B stale page'], 25, 10))
    }
    expect(screen.getByText('B public feed')).toBeInTheDocument()
    expect(screen.queryByText('B stale page')).not.toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    watcher.stop()
  })

  it("shows neither A's nor B's rows under C when B answers after a second switch", async () => {
    showList()
    await answer(onlyCallFor(TENANT_A.id), listBody(TENANT_A.id, A_NAMES))
    expect(await screen.findByText('A secret orders')).toBeInTheDocument()

    const watcher = watchFor([...A_NAMES, ...B_NAMES])
    await switchTo(TENANT_B)
    const callB = onlyCallFor(TENANT_B.id)
    await switchTo(TENANT_C)
    const callC = onlyCallFor(TENANT_C.id)

    await answer(callB, listBody(TENANT_B.id, B_NAMES))
    expect(watcher.seen()).toEqual([])
    expect(skeletonShown()).toBe(true)

    await answer(callC, listBody(TENANT_C.id, ['C only row']))
    expect(await screen.findByText('C only row')).toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    watcher.stop()
    expect(errorNotifications()).toEqual([])
  })

  it('keeps the skeleton up when a skipped page answers while a later page loads', async () => {
    // Page 1 loaded, Next twice (page 2 then page 3 pending), page 2's
    // answer lands first. The list must keep waiting for page 3 rather
    // than show page 1's rows under "Page 3 of 3".
    showList()
    const page1Names = Array.from({ length: 10 }, (_, i) => `Page one row ${i}`)
    await answer(calls[0], listBody(TENANT_A.id, page1Names, 25, 0))
    expect(await screen.findByText('Page 1 of 3')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await flush()
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await flush()
    expect(calls).toHaveLength(3)
    expect(calls[1].url).toContain('offset=10')
    expect(calls[2].url).toContain('offset=20')
    expect(screen.getByText('Page 3 of 3')).toBeInTheDocument()

    const watcher = watchFor(page1Names)
    await answer(calls[1], listBody(TENANT_A.id, ['Page two row 0'], 25, 10))
    expect(skeletonShown()).toBe(true)
    expect(watcher.seen()).toEqual([])
    expect(screen.queryByText('Page two row 0')).not.toBeInTheDocument()

    await answer(calls[2], listBody(TENANT_A.id, ['Page three row 0'], 25, 20))
    expect(await screen.findByText('Page three row 0')).toBeInTheDocument()
    expect(watcher.seen()).toEqual([])
    watcher.stop()
  })
})

// ---- #515 -----------------------------------------------------------------

// The list's own requests ask for 10 per page; the header summary that a
// delete refreshes asks for 100. Keep them apart.
const pageCalls = () => calls.filter((c) => c.url.includes('limit=10&'))
const lastPageCall = (): PendingCall => {
  const page = pageCalls()
  expect(page.length).toBeGreaterThan(0)
  return page[page.length - 1]
}
const summaryCalls = () => calls.filter((c) => c.url.includes('limit=100&'))

// Rows numbered explicitly, so a fresh load after a delete can leave the
// deleted one out without the ids shifting onto other rows.
const rowName = (tenantId: string, n: number) => `${tenantId} row ${n}`
const rowsBody = (tenantId: string, nums: number[], total: number, offset = 0) => ({
  data: nums.map((n) => wireConnection(tenantId, `${tenantId}-conn-${n}`, rowName(tenantId, n))),
  total,
  limit: 10,
  offset,
})
const range = (from: number, to: number) => Array.from({ length: to - from }, (_, i) => from + i)

interface PendingDelete extends Deferred {
  url: string
}
let deletes: PendingDelete[] = []

const rowOf = (name: string) => screen.getByText(name).closest('.card-elevated') as HTMLElement

// Click Delete on a row and confirm. The DELETE request stays open until the
// test settles it; `done` is the confirm handler finishing.
const startDelete = async (name: string): Promise<{ request: PendingDelete; done: Promise<void> }> => {
  fireEvent.click(within(rowOf(name)).getByRole('button', { name: 'Delete' }))
  const dialog = useUIStore.getState().confirmDialog
  expect(dialog).not.toBeNull()
  let done: Promise<void> = Promise.resolve()
  await act(async () => {
    done = Promise.resolve(dialog!.onConfirm())
  })
  await flush()
  expect(deletes).toHaveLength(1)
  return { request: deletes[0], done }
}
const settleDelete = async (d: { request: PendingDelete; done: Promise<void> }) => {
  d.request.resolve({ data: {} })
  await flush()
  await act(async () => {
    await d.done
  })
}

const successNotifications = () => useUIStore.getState().notifications.filter((n) => n.type === 'success')
const pagination = () => screen.queryByText(/^Page \d+ of \d+$/)?.textContent ?? null
const errorState = () => screen.queryByText("Couldn't load connections")
const tryAgain = () => screen.queryByRole('button', { name: 'Try again' })

describe('ConnectionsList: a late delete and a failed load (#515)', () => {
  beforeEach(() => {
    deletes = []
    del.mockReset()
    del.mockImplementation(((url: string) =>
      new Promise((resolve, reject) => {
        deletes.push({ url, resolve, reject })
      })) as never)
    useConnectionsStore.setState(useConnectionsStore.getInitialState(), true)
  })

  describe('delete answering after a reload', () => {
    it("leaves B's total and rows alone when A's delete answers after B loaded", async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, [0, 1], 2))
      await screen.findByText(rowName(TENANT_A.id, 0))

      const pending = await startDelete(rowName(TENANT_A.id, 0))
      expect(pending.request.url).toBe(`/api/v1/connections/${TENANT_A.id}-conn-0`)

      // B has 11 connections: two pages.
      await switchTo(TENANT_B)
      const callB = lastPageCall()
      expect(callB.tenantId).toBe(TENANT_B.id)
      await answer(callB, rowsBody(TENANT_B.id, range(0, 10), 11))
      expect(await screen.findByText(rowName(TENANT_B.id, 0))).toBeInTheDocument()
      expect(pagination()).toBe('Page 1 of 2')
      const summariesBefore = summaryCalls().length

      await settleDelete(pending)

      // B's total is what B's load said: 11 is still two pages. Subtracting
      // A's delete would make it 10 and the pagination would vanish.
      expect(pagination()).toBe('Page 1 of 2')
      for (const n of range(0, 10)) expect(screen.getByText(rowName(TENANT_B.id, n))).toBeInTheDocument()
      // The delete did happen: the user is told, and the summary refreshes.
      expect(successNotifications()).toHaveLength(1)
      expect(successNotifications()[0]).toMatchObject({
        title: 'Deleted',
        message: `"${rowName(TENANT_A.id, 0)}" deleted`,
      })
      expect(summaryCalls()).toHaveLength(summariesBefore + 1)
      expect(errorNotifications()).toEqual([])
    })

    it("keeps A's fresh total after A, B, back to A when the delete answers last", async () => {
      // A workspace-id check would pass here: the delete started in A and
      // answers in A. Only "a load landed since" catches it.
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, range(0, 10), 12))
      expect(await screen.findByText(rowName(TENANT_A.id, 0))).toBeInTheDocument()
      expect(pagination()).toBe('Page 1 of 2')

      const pending = await startDelete(rowName(TENANT_A.id, 0))

      await switchTo(TENANT_B)
      await answer(lastPageCall(), listBody(TENANT_B.id, B_NAMES))
      expect(await screen.findByText('B public feed')).toBeInTheDocument()

      // Back to A. The server has already deleted row 0: A's fresh load
      // has 11 connections, rows 1 to 10 on page 1.
      await switchTo(TENANT_A)
      const freshA = lastPageCall()
      expect(freshA.tenantId).toBe(TENANT_A.id)
      expect(freshA.url).toContain('offset=0')
      await answer(freshA, rowsBody(TENANT_A.id, range(1, 11), 11))
      expect(await screen.findByText(rowName(TENANT_A.id, 10))).toBeInTheDocument()
      expect(pagination()).toBe('Page 1 of 2')

      await settleDelete(pending)

      expect(pagination()).toBe('Page 1 of 2')
      for (const n of range(1, 11)) expect(screen.getByText(rowName(TENANT_A.id, n))).toBeInTheDocument()
      expect(screen.queryByText('B public feed')).not.toBeInTheDocument()
      expect(successNotifications()).toHaveLength(1)
    })

    it('keeps the new page total when a page change lands before the delete answers', async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, range(0, 10), 12))
      expect(await screen.findByText(rowName(TENANT_A.id, 0))).toBeInTheDocument()
      expect(pagination()).toBe('Page 1 of 2')

      const pending = await startDelete(rowName(TENANT_A.id, 0))

      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await flush()
      const page2 = lastPageCall()
      expect(page2.url).toContain('offset=10')
      // Row 0 is gone on the server: 11 left, page 2 holds row 11.
      await answer(page2, rowsBody(TENANT_A.id, [11], 11, 10))
      expect(await screen.findByText(rowName(TENANT_A.id, 11))).toBeInTheDocument()
      expect(pagination()).toBe('Page 2 of 2')

      await settleDelete(pending)

      // 11 - 1 = 10 would be one page and the pagination would vanish.
      expect(pagination()).toBe('Page 2 of 2')
      expect(screen.getByText(rowName(TENANT_A.id, 11))).toBeInTheDocument()
      expect(successNotifications()).toHaveLength(1)
    })

    it('keeps the retried total when a retry lands before the delete answers', async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, range(0, 10), 12))
      expect(await screen.findByText(rowName(TENANT_A.id, 0))).toBeInTheDocument()

      const pending = await startDelete(rowName(TENANT_A.id, 0))

      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await flush()
      await fail(lastPageCall(), new Error('network down'))
      expect(errorState()).toBeInTheDocument()

      fireEvent.click(tryAgain()!)
      await flush()
      const retry = lastPageCall()
      expect(retry.url).toContain('offset=10')
      await answer(retry, rowsBody(TENANT_A.id, [11], 11, 10))
      expect(await screen.findByText(rowName(TENANT_A.id, 11))).toBeInTheDocument()
      expect(pagination()).toBe('Page 2 of 2')

      await settleDelete(pending)

      expect(pagination()).toBe('Page 2 of 2')
      expect(screen.getByText(rowName(TENANT_A.id, 11))).toBeInTheDocument()
      expect(successNotifications()).toHaveLength(1)
    })
  })

  describe('delete answering with no reload in between', () => {
    it('removes the row and lowers the total, as before', async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, range(0, 10), 11))
      expect(await screen.findByText(rowName(TENANT_A.id, 3))).toBeInTheDocument()
      expect(pagination()).toBe('Page 1 of 2')

      const pending = await startDelete(rowName(TENANT_A.id, 3))
      // Still pending: nothing changed yet.
      expect(screen.getByText(rowName(TENANT_A.id, 3))).toBeInTheDocument()

      await settleDelete(pending)

      expect(screen.queryByText(rowName(TENANT_A.id, 3))).not.toBeInTheDocument()
      for (const n of range(0, 10).filter((n) => n !== 3)) {
        expect(screen.getByText(rowName(TENANT_A.id, n))).toBeInTheDocument()
      }
      // 11 - 1 = 10 fits on one page.
      expect(pagination()).toBeNull()
      expect(successNotifications()).toHaveLength(1)
      // The list itself wasn't reloaded: the local edit is what changed it.
      expect(pageCalls()).toHaveLength(1)
    })

    it('also works for a delete made after a workspace switch and a page change', async () => {
      // Loads that landed before the delete started must not count against it.
      showList()
      await answer(lastPageCall(), listBody(TENANT_A.id, A_NAMES))
      await switchTo(TENANT_B)
      await answer(lastPageCall(), rowsBody(TENANT_B.id, range(0, 10), 21))
      expect(await screen.findByText('Page 1 of 3')).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await flush()
      await answer(lastPageCall(), rowsBody(TENANT_B.id, range(10, 20), 21, 10))
      expect(await screen.findByText(rowName(TENANT_B.id, 15))).toBeInTheDocument()
      expect(pagination()).toBe('Page 2 of 3')

      await settleDelete(await startDelete(rowName(TENANT_B.id, 15)))

      expect(screen.queryByText(rowName(TENANT_B.id, 15))).not.toBeInTheDocument()
      // 21 - 1 = 20 is two pages.
      expect(pagination()).toBe('Page 2 of 2')
    })
  })

  describe('failed load', () => {
    it('shows an error state with the reason and "Try again", never "No connections yet"', async () => {
      const emptyWatcher = watchFor(['No connections yet'])
      showList()
      await fail(lastPageCall(), new VRSkyAPIError('Workspace suspended', 'HTTP_402'))

      expect(skeletonShown()).toBe(false)
      const alert = screen.getByRole('alert')
      expect(within(alert).getByText("Couldn't load connections")).toBeInTheDocument()
      expect(within(alert).getByText('Workspace suspended')).toBeInTheDocument()
      expect(within(alert).getByRole('button', { name: 'Try again' })).toBeInTheDocument()
      expect(emptyWatcher.seen()).toEqual([])
      emptyWatcher.stop()
      // The toast stays.
      expect(errorNotifications()).toHaveLength(1)
      expect(errorNotifications()[0]).toMatchObject({ title: 'Error', message: 'Workspace suspended' })
    })

    it('uses the generic message for a non-API error', async () => {
      showList()
      await fail(lastPageCall(), 'boom')
      expect(within(screen.getByRole('alert')).getByText('Failed to load connections')).toBeInTheDocument()
      expect(tryAgain()).toBeInTheDocument()
      expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
    })

    it("replaces the previous page's rows with the error state when a page change fails", async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, range(0, 10), 12))
      expect(await screen.findByText(rowName(TENANT_A.id, 0))).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await flush()
      await fail(lastPageCall(), new Error('network down'))

      expect(errorState()).toBeInTheDocument()
      expect(screen.queryByText(rowName(TENANT_A.id, 0))).not.toBeInTheDocument()
      expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
    })

    it("never shows A's error state under B after a switch", async () => {
      showList()
      await fail(lastPageCall(), new VRSkyAPIError('Workspace A suspended', 'HTTP_402'))
      expect(errorState()).toBeInTheDocument()
      expect(screen.getByText('Workspace A suspended')).toBeInTheDocument()

      // Watch from before the switch, so the commit that switches is checked
      // too, before B's request has been answered.
      const watcher = watchFor(["Couldn't load connections", 'Workspace A suspended'])
      await switchTo(TENANT_B)

      expect(watcher.seen()).toEqual([])
      expect(skeletonShown()).toBe(true)

      const callB = lastPageCall()
      expect(callB.tenantId).toBe(TENANT_B.id)
      await answer(callB, listBody(TENANT_B.id, B_NAMES))
      expect(await screen.findByText('B public feed')).toBeInTheDocument()
      expect(watcher.seen()).toEqual([])
      expect(tryAgain()).not.toBeInTheDocument()
      watcher.stop()
    })

    it('still says "No connections yet" for a workspace that really has none', async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, [], 0))
      expect(await screen.findByText('No connections yet')).toBeInTheDocument()
      expect(errorState()).not.toBeInTheDocument()
      expect(tryAgain()).not.toBeInTheDocument()
    })
  })

  describe('Try again', () => {
    it('loads the same page of the same workspace again and shows the rows', async () => {
      showList()
      await answer(lastPageCall(), rowsBody(TENANT_A.id, range(0, 10), 12))
      expect(await screen.findByText(rowName(TENANT_A.id, 0))).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Next' }))
      await flush()
      const failed = lastPageCall()
      await fail(failed, new Error('network down'))
      expect(errorState()).toBeInTheDocument()

      const before = pageCalls().length
      fireEvent.click(tryAgain()!)
      await flush()

      expect(pageCalls()).toHaveLength(before + 1)
      const retry = lastPageCall()
      expect(retry.url).toBe(failed.url)
      expect(retry.url).toBe('/api/v1/connections?limit=10&offset=10')
      expect(retry.tenantId).toBe(TENANT_A.id)
      // While it loads, the skeleton replaces the error state.
      expect(skeletonShown()).toBe(true)
      expect(errorState()).not.toBeInTheDocument()

      await answer(retry, rowsBody(TENANT_A.id, [10, 11], 12, 10))
      expect(await screen.findByText(rowName(TENANT_A.id, 10))).toBeInTheDocument()
      expect(screen.getByText(rowName(TENANT_A.id, 11))).toBeInTheDocument()
      expect(errorState()).not.toBeInTheDocument()
      expect(tryAgain()).not.toBeInTheDocument()
      expect(pagination()).toBe('Page 2 of 2')
      expect(errorNotifications()).toHaveLength(1)
    })

    it('retries the workspace that failed, after a switch', async () => {
      showList()
      await answer(lastPageCall(), listBody(TENANT_A.id, A_NAMES))
      await switchTo(TENANT_B)
      const failed = lastPageCall()
      expect(failed.tenantId).toBe(TENANT_B.id)
      await fail(failed, new Error('network down'))
      expect(errorState()).toBeInTheDocument()

      fireEvent.click(tryAgain()!)
      await flush()
      const retry = lastPageCall()
      expect(retry).not.toBe(failed)
      expect(retry.tenantId).toBe(TENANT_B.id)
      expect(retry.url).toBe('/api/v1/connections?limit=10&offset=0')

      await answer(retry, listBody(TENANT_B.id, B_NAMES))
      expect(await screen.findByText('B public feed')).toBeInTheDocument()
      for (const name of A_NAMES) expect(screen.queryByText(name)).not.toBeInTheDocument()
      expect(errorState()).not.toBeInTheDocument()
    })

    it('shows the error state again when the retry fails too, and can retry again', async () => {
      showList()
      await fail(lastPageCall(), new VRSkyAPIError('Database unavailable', 'HTTP_503'))
      expect(errorState()).toBeInTheDocument()

      fireEvent.click(tryAgain()!)
      await flush()
      expect(errorState()).not.toBeInTheDocument()
      await fail(lastPageCall(), new VRSkyAPIError('Still unavailable', 'HTTP_503'))

      expect(errorState()).toBeInTheDocument()
      expect(within(screen.getByRole('alert')).getByText('Still unavailable')).toBeInTheDocument()
      expect(screen.queryByText('Database unavailable')).not.toBeInTheDocument()
      expect(screen.queryByText('No connections yet')).not.toBeInTheDocument()
      expect(errorNotifications().map((n) => n.message)).toEqual(['Database unavailable', 'Still unavailable'])

      fireEvent.click(tryAgain()!)
      await flush()
      expect(pageCalls()).toHaveLength(3)
      await answer(lastPageCall(), listBody(TENANT_A.id, A_NAMES))
      expect(await screen.findByText('A secret orders')).toBeInTheDocument()
      expect(errorState()).not.toBeInTheDocument()
    })
  })
})
