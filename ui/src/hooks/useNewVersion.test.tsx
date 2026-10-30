/**
 * An open tab notices a newer deployed UI (#551).
 *
 * The tab's own assets come from `document` on mount, so each test puts the
 * built entry script and stylesheet into <head> first. `fetch` is stubbed and
 * answers with an index.html; timers and Date are fake.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'

// Every state update the hook makes, so a test can tell that a late answer
// set nothing (React 18 drops an update on an unmounted component silently).
const stateUpdates = vi.hoisted(() => ({ count: 0 }))
vi.mock('react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react')>()
  const useState = ((init: unknown) => {
    const [value, set] = actual.useState(init)
    const counted = (next: unknown) => {
      stateUpdates.count++
      set(next)
    }
    return [value, counted]
  }) as typeof actual.useState
  return { ...actual, default: { ...actual, useState }, useState }
})

import {
  useNewVersion,
  VERSION_POLL_MS,
  VERSION_FOCUS_GAP_MS,
  VERSION_CONFIRM_GAP_MS,
  VERSION_RECHECK_MS,
} from './useNewVersion'

const A = ['/assets/index-AAA.js', '/assets/index-AAA.css']
const B = ['/assets/index-BBB.js', '/assets/index-AAA.css']
const C = ['/assets/index-CCC.js', '/assets/index-CCC.css']

const html = (assets: string[]) =>
  `<!doctype html><html><head>${assets
    .map((a) => (a.endsWith('.css') ? `<link rel="stylesheet" href="${a}">` : `<script type="module" src="${a}"></script>`))
    .join('')}</head><body><div id="root"></div></body></html>`

const answer = (body: string, status = 200) => ({
  ok: status >= 200 && status < 300,
  status,
  text: async () => body,
})

const fetchMock = vi.fn()
let visibility: DocumentVisibilityState = 'visible'
const added: Element[] = []

/** The running tab loaded these assets. */
function loadTab(assets: string[]) {
  for (const a of assets) {
    const el = a.endsWith('.css') ? document.createElement('link') : document.createElement('script')
    if (el instanceof HTMLLinkElement) {
      el.rel = 'stylesheet'
      el.href = a
    } else {
      el.setAttribute('type', 'module')
      el.setAttribute('src', a)
    }
    document.head.appendChild(el)
    added.push(el)
  }
}

/** Advance fake time and let the fetch/text promises settle. */
async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

function setVisibility(state: DocumentVisibilityState) {
  visibility = state
}

async function fire(target: 'visibilitychange' | 'focus') {
  await act(async () => {
    if (target === 'focus') window.dispatchEvent(new Event('focus'))
    else document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
  stateUpdates.count = 0
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  added.splice(0).forEach((el) => el.remove())
  delete (document as unknown as Record<string, unknown>).visibilityState
})

describe('useNewVersion: the comparison', () => {
  it('reports the deployed assets when they differ from the tab’s', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeTruthy()
    expect(result.current).toContain('/assets/index-BBB.js')
  })

  it('reports nothing when the deployed assets are the tab’s own, in any order', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html([...A].reverse())))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(result.current).toBeNull()
  })

  it('reports a later, different version again', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    const first = result.current
    expect(first).toContain('index-BBB.js')
    fetchMock.mockResolvedValue(answer(html(C)))
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-CCC.js')
    expect(result.current).not.toBe(first)
  })
})

describe('useNewVersion: two matching checks (#600)', () => {
  it('offers nothing after one check that sees a new set', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(result.current).toBeNull()
    // Nothing more happens by itself before the confirming check is due.
    await advance(VERSION_CONFIRM_GAP_MS - 1_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(result.current).toBeNull()
  })

  it('offers the version once a second check at least 30 s later sees the same set', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(VERSION_RECHECK_MS).toBeGreaterThanOrEqual(VERSION_CONFIRM_GAP_MS)
    expect(result.current).toContain('index-BBB.js')
  })

  it('does not count a second check less than 30 s after the first', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(A)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS) // check at 5:00, same assets
    // A rollout starts. A focus check at 9:40 is the first sighting...
    fetchMock.mockResolvedValue(answer(html(B)))
    await advance(VERSION_POLL_MS - 20_000)
    await fire('focus')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeNull()
    // ...and the timer check at 10:00 is only 20 s later.
    await advance(20_000)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toBeNull()
    // The confirming check, 31 s after the first sighting, counts.
    await advance(VERSION_RECHECK_MS - 20_000)
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(result.current).toContain('index-BBB.js')
  })

  it('restarts the wait when the set changes between the checks', async () => {
    loadTab(A)
    fetchMock.mockResolvedValueOnce(answer(html(B))).mockResolvedValue(answer(html(C)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS) // B
    await advance(VERSION_RECHECK_MS) // C: not B twice
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS) // C twice
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toContain('index-CCC.js')
    expect(result.current).not.toContain('index-BBB.js')
  })

  it('does not offer a version that went away again (rollback)', async () => {
    loadTab(A)
    fetchMock.mockResolvedValueOnce(answer(html(B))).mockResolvedValue(answer(html(A)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toBeNull()
  })

  it('after a rollback to the old set, needs two matching sightings of the new set again', async () => {
    loadTab(A)
    // The old pod answers the confirming check, then the new one answers again.
    fetchMock
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer(html(A)))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS) // B: first sighting
    await advance(VERSION_RECHECK_MS) // A: the old pod answered
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS - VERSION_RECHECK_MS) // B again, but a first sighting
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS) // B twice since the rollback
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(result.current).toContain('index-BBB.js')
  })

  it('a rejected fetch between two sightings starts the wait again', async () => {
    loadTab(A)
    fetchMock
      .mockResolvedValueOnce(answer(html(B)))
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS) // B
    await advance(VERSION_RECHECK_MS) // network error
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS - VERSION_RECHECK_MS) // B again, but a first sighting
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-BBB.js')
  })

  it('a page without /assets/ references between two sightings starts the wait again', async () => {
    loadTab(A)
    fetchMock
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer('<html><body><h1>503 Service Temporarily Unavailable</h1></body></html>'))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS) // B
    await advance(VERSION_RECHECK_MS) // an error page
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS - VERSION_RECHECK_MS) // B again, but a first sighting
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-BBB.js')
  })

  it('withdraws an offered version when the next check sees yet another set', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-BBB.js')
    fetchMock.mockResolvedValue(answer(html(C)))
    await advance(VERSION_POLL_MS - VERSION_RECHECK_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-CCC.js')
  })

  it('a failed check between two sightings starts the wait again', async () => {
    loadTab(A)
    fetchMock
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer('', 503))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS) // B
    await advance(VERSION_RECHECK_MS) // 503
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS - VERSION_RECHECK_MS + 1_000) // B again, but a first sighting
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toBeNull()
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-BBB.js')
  })

  it('does not make the confirming check while the tab is hidden', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    setVisibility('hidden')
    await advance(VERSION_FOCUS_GAP_MS + 1_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(result.current).toBeNull()
    // Back in view, the next check confirms.
    setVisibility('visible')
    await fire('focus')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toContain('index-BBB.js')
  })
})

describe('useNewVersion: failed or unusable answers', () => {
  it('ignores a rejected fetch, and the next check still runs', async () => {
    loadTab(A)
    fetchMock
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toContain('index-BBB.js')
  })

  it('ignores a non-2xx answer whatever its body, and the next check still runs', async () => {
    loadTab(A)
    // A 503 from the ingress while the cluster is parked; even if the body
    // looked like a different build, it is not the deployed UI.
    fetchMock
      .mockResolvedValueOnce(answer(html(B), 503))
      .mockResolvedValueOnce(answer(html(C), 404))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(result.current).toContain('index-BBB.js')
  })

  it('ignores a page without /assets/ references, and the next check still runs', async () => {
    loadTab(A)
    fetchMock
      .mockResolvedValueOnce(answer('<html><body><h1>503 Service Temporarily Unavailable</h1></body></html>'))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(result.current).toContain('index-BBB.js')
  })

  it('keeps checking after a request that never settles', async () => {
    loadTab(A)
    fetchMock.mockReturnValueOnce(new Promise(() => {})).mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(result.current).toBeNull()
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current).toContain('index-BBB.js')
  })

  it('keeps a found version when a later answer has no /assets/ references or fails', async () => {
    loadTab(A)
    fetchMock
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer(html(B)))
      .mockResolvedValueOnce(answer('<html><body>bad gateway</body></html>'))
      .mockResolvedValueOnce(answer('', 502))
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    await advance(VERSION_RECHECK_MS)
    const found = result.current
    expect(found).toContain('index-BBB.js')
    await advance(VERSION_POLL_MS)
    expect(result.current).toBe(found)
    await advance(VERSION_POLL_MS)
    expect(result.current).toBe(found)
    await advance(VERSION_POLL_MS)
    // A good answer for the same set after the failures keeps it too.
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(6)
    expect(result.current).toBe(found)
  })
})

describe('useNewVersion: when it checks', () => {
  it('checks every 5 minutes while visible, not before', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(A)))
    renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS - 1)
    expect(fetchMock).not.toHaveBeenCalled()
    await advance(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await advance(VERSION_POLL_MS)
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('makes no timer request while the tab is hidden', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    setVisibility('hidden')
    await advance(VERSION_POLL_MS * 4)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current).toBeNull()
  })

  it('checks when the tab becomes visible again after more than a minute', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    setVisibility('hidden')
    await fire('visibilitychange')
    await advance(VERSION_FOCUS_GAP_MS + 1_000)
    expect(fetchMock).not.toHaveBeenCalled()
    setVisibility('visible')
    await fire('visibilitychange')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toContain('index-BBB.js')
  })

  it('checks on focus after more than a minute', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_FOCUS_GAP_MS + 1_000)
    await fire('focus')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current).toContain('index-BBB.js')
  })

  it('counts page load as a check: no request on focus or visibility within a minute of mount', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    renderHook(() => useNewVersion())
    await advance(VERSION_FOCUS_GAP_MS / 2)
    await fire('focus')
    await fire('visibilitychange')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('makes no request on focus within a minute of the last timer check', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(A)))
    renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await advance(VERSION_FOCUS_GAP_MS - 5_000)
    await fire('focus')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await advance(10_000)
    await fire('focus')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('makes no request when focus fires while the tab is hidden', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    renderHook(() => useNewVersion())
    await advance(VERSION_FOCUS_GAP_MS * 2)
    setVisibility('hidden')
    await fire('focus')
    await fire('visibilitychange')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('useNewVersion: the request', () => {
  it('bypasses caches: no-store and a query that differs per request', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(A)))
    renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    await advance(VERSION_POLL_MS)
    await advance(VERSION_FOCUS_GAP_MS + 1)
    await fire('focus')
    expect(fetchMock).toHaveBeenCalledTimes(3)
    const urls = fetchMock.mock.calls.map(([url]) => String(url))
    for (const [url, init] of fetchMock.mock.calls) {
      expect(String(url)).toMatch(/index\.html\?/)
      expect(init).toMatchObject({ cache: 'no-store' })
    }
    expect(new Set(urls).size).toBe(urls.length)
  })
})

describe('useNewVersion: no built assets', () => {
  it('never fetches when the document has no /assets/ references', async () => {
    // Like the Vite dev server: the entry is /src/main.tsx.
    const dev = document.createElement('script')
    dev.setAttribute('type', 'module')
    dev.setAttribute('src', '/src/main.tsx')
    document.head.appendChild(dev)
    added.push(dev)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { result } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS * 3)
    await fire('focus')
    setVisibility('hidden')
    await fire('visibilitychange')
    setVisibility('visible')
    await fire('visibilitychange')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current).toBeNull()
  })
})

describe('useNewVersion: unmount', () => {
  it('stops the timer and the focus and visibility listeners', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { unmount } = renderHook(() => useNewVersion())
    unmount()
    await advance(VERSION_POLL_MS * 3)
    await fire('focus')
    setVisibility('hidden')
    await fire('visibilitychange')
    setVisibility('visible')
    await fire('visibilitychange')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('cancels a pending confirming check', async () => {
    loadTab(A)
    fetchMock.mockResolvedValue(answer(html(B)))
    const { unmount } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    await advance(VERSION_RECHECK_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('sets no state from an answer that arrives after unmount', async () => {
    loadTab(A)
    let release: (v: unknown) => void = () => {}
    fetchMock.mockReturnValue(new Promise((resolve) => { release = resolve }))
    const { result, unmount } = renderHook(() => useNewVersion())
    await advance(VERSION_POLL_MS)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    unmount()
    const before = stateUpdates.count
    await act(async () => {
      release(answer(html(B)))
      await vi.advanceTimersByTimeAsync(0)
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(stateUpdates.count).toBe(before)
    expect(result.current).toBeNull()
  })
})
