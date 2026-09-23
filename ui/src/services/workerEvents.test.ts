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
})
