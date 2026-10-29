/**
 * #501: only the latest list load may write.
 *
 * The list reloads when the page or the workspace changes. On a slow switch
 * from workspace A to B, A's answer could land after B's and the list showed
 * A's connections under B. These tests hold every list request open and
 * answer them in the bad order. Only the HTTP client is mocked; the real
 * component, router and stores run.
 */

import { Profiler } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock(import('@/services/api'), async (importOriginal) => ({
  ...(await importOriginal()),
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } as never,
}))

import apiClient from '@/services/api'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import type { Tenant } from '@/types/models'
import ConnectionsList from './ConnectionsList'

const get = vi.mocked(apiClient.get)

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
    // The failed load is B's answer: the page settles on B's (empty) list
    // instead of hanging on the skeleton.
    expect(skeletonShown()).toBe(false)
    expect(screen.getByText('No connections yet')).toBeInTheDocument()
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
