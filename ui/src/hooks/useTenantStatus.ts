/**
 * useTenantStatus
 * SSE hook for streaming tenant provisioning status updates
 */

import { useState, useEffect } from 'react'
import { config } from '@/config/env'
import type { ProvisioningStatus } from '@/types/models'

const SESSION_KEY = 'vrsky_session_token'

export function useTenantStatus(tenantId: string | null) {
  const [status, setStatus] = useState<ProvisioningStatus | null>(null)

  useEffect(() => {
    if (!tenantId) return

    const token = sessionStorage.getItem(SESSION_KEY)
    if (!token) return

    // EventSource doesn't support custom headers, so we append the token as a query param.
    // The backend SSE handler is behind SessionAuthMiddleware which reads Bearer from Authorization header,
    // so we use a fetch-based SSE approach instead.
    const abortController = new AbortController()

    const connectSSE = async () => {
      try {
        const response = await fetch(
          `${config.apiUrl}/api/v1/tenants/${tenantId}/status/stream`,
          {
            headers: {
              'Authorization': `Bearer ${token}`,
              'Accept': 'text/event-stream',
            },
            signal: abortController.signal,
          }
        )

        if (!response.ok || !response.body) return

        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            if (line.startsWith('data: ')) {
              try {
                const msg = JSON.parse(line.slice(6))
                if (msg.type === 'status' && msg.data) {
                  setStatus(msg.data as ProvisioningStatus)
                }
               } catch (err) {
                 if (import.meta.env.MODE !== 'production') {
                   console.error('Failed to parse SSE status message:', { error: err, line })
                 }
                 // skip invalid JSON
               }
            }
          }
        }
      } catch (err) {
        if ((err as Error).name !== 'AbortError') {
          // Connection closed or error — ignore silently
        }
      }
    }

    connectSSE()

    return () => {
      abortController.abort()
    }
  }, [tenantId])

  return status
}
