import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

import { subscribeWorkerEvents } from './workerEvents'

vi.mock('@/config/env', () => ({ config: { apiUrl: 'http://api.test' } }))
vi.mock('@/services/api', () => ({ getActiveTenantId: () => 'tenant-1' }))
vi.mock('@/services/authService', () => ({ getSessionToken: () => 'tok' }))

/** A response body that emits the given frames and then ends, like a proxy
 *  closing an idle SSE stream. */
function streamOf(...frames: string[]): Response {
  const encoder = new TextEncoder()
  let i = 0
  return {
    ok: true,
    status: 200,
    body: {
      getReader: () => ({
        read: async () =>
          i < frames.length
            ? { done: false, value: encoder.encode(frames[i++]) }
            : { done: true, value: undefined },
      }),
    },
  } as unknown as Response
}

const event = (payload: unknown) => `data: ${JSON.stringify(payload)}\n\n`

/** Let the subscriber's async loop run, including its reconnect pause. */
const settle = async (ms = 800) => {
  await vi.advanceTimersByTimeAsync(ms)
}

describe('subscribeWorkerEvents', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  // The bug this file exists for: a worker stream that ends is routine — the
  // proxy times out, a pod restarts — and the panel used to go silent for the
  // rest of the session, which looks exactly like a pipeline with no traffic.
  it('reconnects after the stream ends, and delivers events from the new one', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(streamOf(event({ message: 'first' })))
      .mockResolvedValueOnce(streamOf(event({ message: 'second' })))
      .mockResolvedValue(streamOf())
    vi.stubGlobal('fetch', fetchMock)

    const onEvent = vi.fn()
    const stop = subscribeWorkerEvents('conn-1', 'http-producer', { onEvent })

    await settle()
    stop()

    expect(fetchMock.mock.calls.length).toBeGreaterThan(1)
    expect(onEvent.mock.calls.map(([e]) => (e as { message: string }).message)).toContain('second')
  })

  it('stops reconnecting once unsubscribed', async () => {
    const fetchMock = vi.fn().mockResolvedValue(streamOf(event({ message: 'x' })))
    vi.stubGlobal('fetch', fetchMock)

    const stop = subscribeWorkerEvents('conn-1', 'http-producer', { onEvent: vi.fn() })
    await settle(200)
    stop()
    const afterStop = fetchMock.mock.calls.length

    await settle(5_000)
    expect(fetchMock.mock.calls.length).toBe(afterStop)
  })

  // A rejected request will be rejected again: retrying it forever would bury
  // the real problem under noise, so it is reported once and left alone.
  it('does not retry a request the API refused', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 403, body: null } as Response)
    vi.stubGlobal('fetch', fetchMock)

    const onError = vi.fn()
    const stop = subscribeWorkerEvents('conn-1', 'http-producer', { onEvent: vi.fn(), onError })

    await settle(5_000)
    stop()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(onError).toHaveBeenCalledWith(expect.stringContaining('403'))
  })

  // In prod (2026-09-28) the proxy answered every connection with an error
  // frame and closed. That counted as a routine end, so the panel reconnected
  // twice a second and raised a notification each time — a wall of "cannot
  // reach the data-converter service".
  it('backs off and notifies once while the proxy keeps reporting the worker unreachable', async () => {
    const errorFrame = 'event: error\ndata: {"error":"cannot reach the data-converter service"}\n\n'
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(streamOf(errorFrame)))
    vi.stubGlobal('fetch', fetchMock)

    const onError = vi.fn()
    const stop = subscribeWorkerEvents('conn-1', 'data-converter', { onEvent: vi.fn(), onError })
    await settle(10_000)
    stop()

    expect(onError).toHaveBeenCalledTimes(1)
    expect(onError).toHaveBeenCalledWith('cannot reach the data-converter service')
    // Doubling from 1 s: 1, 2, 4, 8 — a handful of attempts in ten seconds, not twenty.
    expect(fetchMock.mock.calls.length).toBeGreaterThan(1)
    expect(fetchMock.mock.calls.length).toBeLessThan(8)
  })

  it('reports the same outage again once events have flowed in between', async () => {
    const errorFrame = 'event: error\ndata: {"error":"cannot reach the data-converter service"}\n\n'
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(streamOf(errorFrame))
      .mockResolvedValueOnce(streamOf(event({ type: 'converted' })))
      .mockResolvedValueOnce(streamOf(errorFrame))
      .mockResolvedValue(streamOf())
    vi.stubGlobal('fetch', fetchMock)

    const onError = vi.fn()
    const onEvent = vi.fn()
    const stop = subscribeWorkerEvents('conn-1', 'data-converter', { onEvent, onError })
    await settle(10_000)
    stop()

    expect(onEvent).toHaveBeenCalledTimes(1)
    expect(onError).toHaveBeenCalledTimes(2)
  })
})
