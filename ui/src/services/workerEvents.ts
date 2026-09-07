/**
 * Live event streams for the pipeline builder's test panels.
 *
 * These panels used to open an EventSource straight at a worker's auxiliary
 * port (http://localhost:9200/events/<id> and friends). That only ever worked
 * in local compose: in a deployment those ports are not reachable from a
 * browser, so the connection failed and retried forever behind an `onerror`
 * handler whose entire body was `// Will auto-reconnect`. The panel opened,
 * showed an empty list, and reported nothing.
 *
 * They now go through the management API, which authenticates the caller and
 * checks the connection belongs to their workspace before proxying — the
 * worker endpoints have no auth of their own.
 *
 * EventSource cannot be used for this. It sends no custom headers, and the API
 * identifies the workspace with X-Tenant-ID, so every request would be
 * rejected as `Missing or invalid tenant ID header`. The alternative would be
 * accepting the workspace from a query parameter, which is a weaker path into
 * a tenant-scoped endpoint for the convenience of one browser API. So this
 * reads the stream with fetch instead, sending the same headers as every other
 * API call, and reimplements the small part of the SSE protocol we use.
 */

import { config } from '@/config/env'
import { getActiveTenantId } from '@/services/api'
import { getSessionToken } from '@/services/authService'

/** Workers that publish a live event stream. Must match the allowlist in
 *  src/pkg/managementapi/worker_events_proxy.go — an unknown name 404s. */
export type EventWorker =
  | 'file-consumer'
  | 'http-producer'
  | 'db-producer'
  | 'data-converter'
  | 'data-filter'

export interface WorkerEventsHandlers {
  /** One decoded `data:` payload. Malformed JSON is skipped, not surfaced. */
  onEvent: (event: unknown) => void
  /** Called when the stream cannot be established or ends unexpectedly.
   *  Unlike the EventSource version this is a real signal — pass something
   *  that reaches the user rather than swallowing it. */
  onError?: (message: string) => void
}

/**
 * Subscribe to a worker's live events for one connection.
 * Returns an unsubscribe function; call it on unmount.
 */
export function subscribeWorkerEvents(
  connectionId: string,
  worker: EventWorker,
  { onEvent, onError }: WorkerEventsHandlers,
): () => void {
  const controller = new AbortController()
  let stopped = false

  const url =
    `${config.apiUrl}/api/v1/connections/${encodeURIComponent(connectionId)}` +
    `/workers/${worker}/events`

  const headers: Record<string, string> = {
    Accept: 'text/event-stream',
    'X-Tenant-ID': getActiveTenantId(),
  }
  const token = getSessionToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  ;(async () => {
    try {
      const resp = await fetch(url, {
        headers,
        credentials: 'include',
        signal: controller.signal,
      })
      if (!resp.ok || !resp.body) {
        onError?.(`Live events unavailable (${resp.status})`)
        return
      }

      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      // SSE frames are separated by a blank line and can be split across
      // chunks, so partial text has to survive between reads.
      let buffer = ''

      for (;;) {
        const { done, value } = await reader.read()
        if (done || stopped) break
        buffer += decoder.decode(value, { stream: true })

        let sep: number
        while ((sep = buffer.indexOf('\n\n')) !== -1) {
          const frame = buffer.slice(0, sep)
          buffer = buffer.slice(sep + 2)

          // We only consume `data:` lines. The proxy also emits
          // `event: error` frames; their data carries the message.
          const data = frame
            .split('\n')
            .filter((l) => l.startsWith('data:'))
            .map((l) => l.slice(5).trim())
            .join('\n')
          if (!data) continue

          try {
            const parsed = JSON.parse(data)
            if (frame.includes('event: error')) {
              onError?.(
                typeof parsed?.error === 'string' ? parsed.error : 'Live event stream ended',
              )
              continue
            }
            onEvent(parsed)
          } catch {
            /* a frame we can't parse is not worth interrupting the stream for */
          }
        }
      }
    } catch (e) {
      // An aborted fetch is the caller unsubscribing, not a failure.
      if (controller.signal.aborted) return
      onError?.(e instanceof Error ? e.message : 'Live event stream failed')
    }
  })()

  return () => {
    stopped = true
    controller.abort()
  }
}
